// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"strings"
	"testing"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// neteyeMasterPolicy is the policy the operator applies to the master realm.
const neteyeMasterPolicy = "length(12) and digits(2) and upperCase(1) and lowerCase(1) and specialChars(1) and passwordAge(120)"

func TestParsePasswordPolicy(t *testing.T) {
	cases := []struct {
		name   string
		policy string
		want   PasswordPolicy
	}{
		{
			name:   "the policy the operator applies to the master realm",
			policy: neteyeMasterPolicy,
			want:   PasswordPolicy{MinLength: 12, Digits: 2, UpperCase: 1, LowerCase: 1, SpecialChars: 1},
		},
		{
			name:   "an empty policy constrains nothing",
			policy: "",
			want:   PasswordPolicy{},
		},
		{
			name:   "rules the operator does not model are ignored",
			policy: "notUsername and notEmail and passwordHistory(3) and hashAlgorithm(pbkdf2-sha512)",
			want:   PasswordPolicy{},
		},
		{
			name:   "maxLength and regexPattern are reported",
			policy: "maxLength(64) and regexPattern(^[a-z].*$)",
			want:   PasswordPolicy{MaxLength: 64, RegexPatterns: []string{"^[a-z].*$"}},
		},
		{
			name:   "a rule with a non-numeric argument is ignored",
			policy: "length(twelve) and digits(3)",
			want:   PasswordPolicy{Digits: 3},
		},
		{
			name:   "surrounding whitespace is tolerated",
			policy: "  length(10)   and   specialChars(2)  ",
			want:   PasswordPolicy{MinLength: 10, SpecialChars: 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParsePasswordPolicy(tc.policy)
			if got.MinLength != tc.want.MinLength || got.MaxLength != tc.want.MaxLength ||
				got.Digits != tc.want.Digits || got.UpperCase != tc.want.UpperCase ||
				got.LowerCase != tc.want.LowerCase || got.SpecialChars != tc.want.SpecialChars {
				t.Errorf("ParsePasswordPolicy(%q) = %+v, want %+v", tc.policy, got, tc.want)
			}
			if strings.Join(got.RegexPatterns, ",") != strings.Join(tc.want.RegexPatterns, ",") {
				t.Errorf("regexPatterns = %v, want %v", got.RegexPatterns, tc.want.RegexPatterns)
			}
		})
	}
}

// classCounts reports how many characters of each class password contains, and
// whether it only uses characters the generator is allowed to emit.
func classCounts(password string) (lower, upper, digits, special int, known bool) {
	known = true
	for i := range len(password) {
		switch character := password[i]; {
		case strings.IndexByte(lowerCaseCharacters, character) >= 0:
			lower++
		case strings.IndexByte(upperCaseCharacters, character) >= 0:
			upper++
		case strings.IndexByte(digitCharacters, character) >= 0:
			digits++
		case strings.IndexByte(specialCharacters, character) >= 0:
			special++
		default:
			known = false
		}
	}
	return lower, upper, digits, special, known
}

func TestGeneratePasswordSatisfiesPolicy(t *testing.T) {
	cases := []struct {
		name       string
		policy     PasswordPolicy
		wantLength int
	}{
		{
			name:       "no policy keeps the default length",
			policy:     PasswordPolicy{},
			wantLength: DefaultGeneratedPasswordLength,
		},
		{
			name:       "the master realm policy",
			policy:     ParsePasswordPolicy(neteyeMasterPolicy),
			wantLength: DefaultGeneratedPasswordLength,
		},
		{
			name:       "a minimum length above the default raises the length",
			policy:     PasswordPolicy{MinLength: 64, Digits: 2, SpecialChars: 3},
			wantLength: 64,
		},
		{
			name:       "a maximum length below the default lowers the length",
			policy:     PasswordPolicy{MinLength: 8, MaxLength: 16, Digits: 2, UpperCase: 1, SpecialChars: 1},
			wantLength: 16,
		},
		{
			name:       "class minimums larger than the default length raise the length",
			policy:     PasswordPolicy{Digits: 20, UpperCase: 20, LowerCase: 20, SpecialChars: 20},
			wantLength: 80,
		},
		{
			name:       "a demanding policy is satisfied exactly at its maximum",
			policy:     PasswordPolicy{MinLength: 12, MaxLength: 12, Digits: 4, UpperCase: 4, LowerCase: 2, SpecialChars: 2},
			wantLength: 12,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The filler characters are drawn at random, so a single sample
			// could pass by luck. Repeat enough to catch a generator that only
			// usually satisfies the policy.
			for range 200 {
				password, err := GeneratePassword(tc.policy)
				if err != nil {
					t.Fatalf("GeneratePassword(%+v) returned %v", tc.policy, err)
				}
				if len(password) != tc.wantLength {
					t.Fatalf("length = %d, want %d (%q)", len(password), tc.wantLength, password)
				}
				lower, upper, digits, special, known := classCounts(password)
				if !known {
					t.Fatalf("password %q contains a character outside the generator alphabet", password)
				}
				if lower < tc.policy.LowerCase {
					t.Fatalf("lowerCase = %d, want >= %d (%q)", lower, tc.policy.LowerCase, password)
				}
				if upper < tc.policy.UpperCase {
					t.Fatalf("upperCase = %d, want >= %d (%q)", upper, tc.policy.UpperCase, password)
				}
				if digits < tc.policy.Digits {
					t.Fatalf("digits = %d, want >= %d (%q)", digits, tc.policy.Digits, password)
				}
				if special < tc.policy.SpecialChars {
					t.Fatalf("specialChars = %d, want >= %d (%q)", special, tc.policy.SpecialChars, password)
				}
				if !isLetter(password[0]) {
					t.Fatalf("password %q does not start with a letter", password)
				}
			}
		})
	}
}

func TestGeneratePasswordOmitsSpecialCharactersWhenThePolicyDoesNotAskForThem(t *testing.T) {
	policy := PasswordPolicy{MinLength: 12, Digits: 2, UpperCase: 1, LowerCase: 1}
	for range 200 {
		password, err := GeneratePassword(policy)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, special, _ := classCounts(password); special != 0 {
			t.Fatalf("password %q contains a special character, want an alphanumeric password", password)
		}
	}
}

func TestGeneratePasswordRejectsAPolicyItCannotSatisfy(t *testing.T) {
	policy := PasswordPolicy{MaxLength: 4, Digits: 2, UpperCase: 2, SpecialChars: 2}
	if _, err := GeneratePassword(policy); err == nil {
		t.Fatal("expected an error for a policy whose minimums exceed its maximum length")
	}
}

func TestGeneratePasswordDoesNotRepeatItself(t *testing.T) {
	policy := ParsePasswordPolicy(neteyeMasterPolicy)
	seen := map[string]bool{}
	for range 100 {
		password, err := GeneratePassword(policy)
		if err != nil {
			t.Fatal(err)
		}
		if seen[password] {
			t.Fatalf("GeneratePassword returned %q twice", password)
		}
		seen[password] = true
	}
}

func TestUserPasswordPolicyReadsTheLiveRealm(t *testing.T) {
	fake := newFakeKeycloakRealms()
	fake.realms["master"] = representation{"realm": "master", "passwordPolicy": neteyeMasterPolicy}
	fake.realms["neteye"] = representation{"realm": "neteye", "passwordPolicy": "length(20) and specialChars(4)"} // #nosec G101 -- False positive
	api := fake.start(t)

	// An empty spec realm resolves to master, where the administrative
	// accounts live.
	policy, err := UserPasswordPolicy(context.Background(), api, neteye.KeycloakUserSpec{Username: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if policy.SpecialChars != 1 || policy.Digits != 2 || policy.MinLength != 12 {
		t.Errorf("master policy = %+v, want the master realm policy", policy)
	}

	policy, err = UserPasswordPolicy(context.Background(), api, neteye.KeycloakUserSpec{Realm: "neteye", Username: "someone"})
	if err != nil {
		t.Fatal(err)
	}
	if policy.MinLength != 20 || policy.SpecialChars != 4 {
		t.Errorf("neteye policy = %+v, want the neteye realm policy", policy)
	}

	if _, err := UserPasswordPolicy(context.Background(), api, neteye.KeycloakUserSpec{Realm: "absent"}); err == nil {
		t.Error("expected an error when the realm does not exist")
	}
}

func TestGeneratePasswordSatisfiesTheLiveRealmPolicy(t *testing.T) {
	fake := newFakeKeycloakRealms()
	fake.realms["master"] = representation{"realm": "master", "passwordPolicy": neteyeMasterPolicy}
	api := fake.start(t)

	policy, err := UserPasswordPolicy(context.Background(), api, neteye.KeycloakUserSpec{Realm: "master"})
	if err != nil {
		t.Fatal(err)
	}
	password, err := GeneratePassword(policy)
	if err != nil {
		t.Fatal(err)
	}
	lower, upper, digits, special, known := classCounts(password)
	if !known || lower < 1 || upper < 1 || digits < 2 || special < 1 || len(password) < 12 {
		t.Errorf("password %q does not satisfy %q", password, neteyeMasterPolicy)
	}
}
