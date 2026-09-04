package cmd

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

func readLine() (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.TrimRight(sb.String(), "\r"), nil
			}
			sb.WriteByte(buf[0])
		}
		if err != nil {
			return sb.String(), err
		}
	}
}

func promptLine(label, def string) string {
	fmt.Printf("%s [%s]: ", label, def)
	line, _ := readLine()
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func promptKey(current string) string {
	display := "not set"
	if current != "" {
		display = maskKey(current)
	}
	fmt.Printf("api key [%s]: ", display)
	entered, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return current
	}
	switch value := strings.TrimSpace(string(entered)); value {
	case "":
		return current
	case "-":
		return ""
	default:
		return value
	}
}
