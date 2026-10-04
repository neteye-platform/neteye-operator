// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

const (
	// DefaultGeneratedPasswordLength matches the 32 characters the NetEye
	// Ansible role generates for the administrative accounts. A realm policy
	// asking for more raises it; a policy asking for less never lowers it.
	DefaultGeneratedPasswordLength = 32

	lowerCaseCharacters = "abcdefghijklmnopqrstuvwxyz"
	upperCaseCharacters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digitCharacters     = "0123456789"
	// specialCharacters are the non-alphanumeric characters that satisfy the
	// Keycloak specialChars rule. The set is deliberately limited to the
	// RFC 3986 unreserved punctuation so the password still survives every
	// consumer that stores it unquoted: none of these characters starts a
	// comment in an ini file, is expanded by a shell, or needs escaping in a
	// URL.
	specialCharacters = "-._~"
)

// PasswordPolicy is the subset of a Keycloak realm password policy that
// constrains a password the operator generates. Rules that a random password
// satisfies by construction, such as notUsername, notEmail, passwordAge and
// passwordHistory, are not represented.
//
// A zero PasswordPolicy imposes no character-class minimum, which reproduces
// the alphanumeric password the operator generated before policies were read.
type PasswordPolicy struct {
	// MinLength is the length(n) rule.
	MinLength int
	// MaxLength is the maxLength(n) rule. Zero means unbounded.
	MaxLength int
	// Digits is the digits(n) rule.
	Digits int
	// UpperCase is the upperCase(n) rule.
	UpperCase int
	// LowerCase is the lowerCase(n) rule.
	LowerCase int
	// SpecialChars is the specialChars(n) rule.
	SpecialChars int
	// RegexPatterns holds the regexPattern(...) rules. The operator cannot
	// satisfy an arbitrary pattern by construction, so it only reports them.
	RegexPatterns []string
}

// ParsePasswordPolicy reads a Keycloak realm passwordPolicy string, for example
// "length(12) and digits(2) and upperCase(1) and lowerCase(1) and
// specialChars(1) and passwordAge(120)". Unknown or valueless rules are
// ignored, so a policy the operator does not model never blocks generation.
func ParsePasswordPolicy(policy string) PasswordPolicy {
	parsed := PasswordPolicy{}
	for _, rule := range strings.Split(policy, " and ") {
		name, argument := splitPolicyRule(strings.TrimSpace(rule))
		if name == "" {
			continue
		}
		if name == "regexPattern" {
			if argument != "" {
				parsed.RegexPatterns = append(parsed.RegexPatterns, argument)
			}
			continue
		}
		value, err := strconv.Atoi(argument)
		if err != nil || value < 0 {
			continue
		}
		switch name {
		case "length":
			parsed.MinLength = value
		case "maxLength":
			parsed.MaxLength = value
		case "digits":
			parsed.Digits = value
		case "upperCase":
			parsed.UpperCase = value
		case "lowerCase":
			parsed.LowerCase = value
		case "specialChars":
			parsed.SpecialChars = value
		}
	}
	return parsed
}

// splitPolicyRule splits "name(argument)" into its parts. A rule without
// parentheses yields an empty argument.
func splitPolicyRule(rule string) (string, string) {
	open := strings.IndexByte(rule, '(')
	if open < 0 || !strings.HasSuffix(rule, ")") {
		return rule, ""
	}
	return rule[:open], rule[open+1 : len(rule)-1]
}

// GetPasswordPolicy reads the live password policy of a realm. The policy is
// read from Keycloak rather than assumed, because an administrator may have
// tightened it and because the realm the account lives in is not necessarily
// the one the operator configures.
func (a *AdminAPI) GetPasswordPolicy(ctx context.Context, realm string) (PasswordPolicy, error) {
	representation, err := a.GetRealm(ctx, realm)
	if err != nil {
		return PasswordPolicy{}, fmt.Errorf("get realm %q: %w", realm, err)
	}
	if representation == nil {
		return PasswordPolicy{}, fmt.Errorf("realm %q not found", realm)
	}
	return ParsePasswordPolicy(stringValue(representation, "passwordPolicy")), nil
}

// UserPasswordPolicy reads the password policy of the realm a KeycloakUser
// account belongs to.
func UserPasswordPolicy(ctx context.Context, api *AdminAPI, spec neteye.KeycloakUserSpec) (PasswordPolicy, error) {
	return api.GetPasswordPolicy(ctx, userRealm(spec))
}

// GeneratePassword returns a random password that satisfies policy.
//
// The required characters are placed first and the remainder is drawn from the
// classes the policy allows, then the whole string is shuffled so the class
// layout carries no information. The first character is always a letter: a
// password starting with "-" would otherwise be read as an option by a consumer
// that passes it on a command line.
func GeneratePassword(policy PasswordPolicy) (string, error) {
	// At least one lower-case letter is always required, because the leading
	// character has to be a letter.
	lowerCase := max(policy.LowerCase, 1)
	required := policy.Digits + policy.UpperCase + lowerCase + policy.SpecialChars

	length := max(DefaultGeneratedPasswordLength, policy.MinLength, required)
	if policy.MaxLength > 0 {
		if required > policy.MaxLength {
			return "", fmt.Errorf("password policy requires at least %d characters but allows at most %d", required, policy.MaxLength)
		}
		length = min(length, policy.MaxLength)
	}

	password := make([]byte, 0, length)
	for class, count := range map[string]int{
		lowerCaseCharacters: lowerCase,
		upperCaseCharacters: policy.UpperCase,
		digitCharacters:     policy.Digits,
		specialCharacters:   policy.SpecialChars,
	} {
		for range count {
			character, err := randomCharacter(class)
			if err != nil {
				return "", err
			}
			password = append(password, character)
		}
	}

	// Only offer special characters to the filler when the policy asks for
	// them, so a realm without a specialChars rule keeps receiving the
	// alphanumeric password its consumers were built for.
	filler := lowerCaseCharacters + upperCaseCharacters + digitCharacters
	if policy.SpecialChars > 0 {
		filler += specialCharacters
	}
	for len(password) < length {
		character, err := randomCharacter(filler)
		if err != nil {
			return "", err
		}
		password = append(password, character)
	}

	if err := shuffle(password); err != nil {
		return "", err
	}
	if err := moveLetterFirst(password); err != nil {
		return "", err
	}
	return string(password), nil
}

// randomCharacter picks one character of alphabet with a uniform distribution.
func randomCharacter(alphabet string) (byte, error) {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
	if err != nil {
		return 0, fmt.Errorf("generate password: %w", err)
	}
	return alphabet[index.Int64()], nil
}

// shuffle permutes password in place with a Fisher-Yates shuffle.
func shuffle(password []byte) error {
	for i := len(password) - 1; i > 0; i-- {
		pick, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return fmt.Errorf("generate password: %w", err)
		}
		j := pick.Int64()
		password[i], password[j] = password[j], password[i]
	}
	return nil
}

// moveLetterFirst swaps the first letter into the leading position. The caller
// guarantees that at least one letter is present.
func moveLetterFirst(password []byte) error {
	for i, character := range password {
		if isLetter(character) {
			password[0], password[i] = password[i], password[0]
			return nil
		}
	}
	return fmt.Errorf("generate password: no letter available for the leading character")
}

func isLetter(character byte) bool {
	return (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
}
