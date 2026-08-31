FROM quay.io/operator-framework/opm:v1.73.0@sha256:e5a6220603fb4504d58c6e3e488386b817e3695c906a62ee0370b5faedc3799a

COPY catalog /configs
LABEL operators.operatorframework.io.index.configs.v1=/configs

USER 1001

ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs"]
