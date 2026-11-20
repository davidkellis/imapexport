_default:
    @just --list

# Build imapexport in the project directory.
build:
    go build -o imapexport .

# Install imapexport into Go's binary directory.
install:
    go install .
