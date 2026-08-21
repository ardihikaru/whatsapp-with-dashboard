// Command hashpw prints a bcrypt hash for WSM_ADMIN_PASSWORD_HASH.
//
//	make hashpw                            # prompts, echo off
//	go run ./cmd/hashpw                    # reads one line from stdin
//	printf '%s' 's3cret' | go run ./cmd/hashpw
//	go run ./cmd/hashpw -cost 12
//
// It reads stdin rather than an argv flag on purpose: a password on the command
// line lands in the shell history and in every `ps` on the box. It does NOT
// suppress echo itself — that needs golang.org/x/term, a dependency this repo
// does not otherwise carry; `make hashpw` wraps it with the shell's own stty.
//
// The Dockerfile builds ./cmd/app only, so this adds nothing to the image.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	cost := flag.Int("cost", bcrypt.DefaultCost, "bcrypt cost (4-31); higher is slower to crack and slower to log in")
	flag.Parse()

	if err := run(*cost); err != nil {
		fmt.Fprintln(os.Stderr, "hashpw:", err)
		os.Exit(1)
	}
}

func run(cost int) error {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return fmt.Errorf("empty password")
	}
	if len([]rune(pw)) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	// GenerateFromPassword rejects input over bcrypt's 72-byte limit rather than
	// silently truncating it — so a long passphrase fails here, loudly, instead
	// of authenticating on its first 72 bytes forever after.
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), cost)
	if err != nil {
		return err
	}
	// Hash to stdout so it can be piped; anything human goes to stderr.
	fmt.Println(string(hash))
	// A bcrypt hash starts "$2a$10$...", and BOTH readers of .env would eat
	// those dollars unquoted: make expands $2/$1 while exporting, and `.` in a
	// POSIX shell expands them as positional parameters. Single quotes survive
	// make, the shell, and docker compose's env_file parser alike — so hand the
	// operator the finished line rather than a value they have to know to quote.
	fmt.Fprintf(os.Stderr, "\nPaste into .env exactly as follows (the quotes matter):\n"+
		"WSM_ADMIN_PASSWORD_HASH='%s'\n", string(hash))
	return nil
}
