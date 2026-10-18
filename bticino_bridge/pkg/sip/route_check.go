package sip

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// CheckAnswererRoute lee route_ext.conf y comprueba si <sip:user@domain> figura en la línea alluser.
func CheckAnswererRoute(routeFile, user, domain string) (bool, error) {
	f, err := os.Open(routeFile)
	if err != nil {
		return false, fmt.Errorf("abriendo %s: %w", routeFile, err)
	}
	defer f.Close()
	aor := fmt.Sprintf("sip:%s@%s", user, domain)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "sip:alluser@"+domain) && strings.Contains(line, aor) {
			return true, nil
		}
	}
	return false, sc.Err()
}
