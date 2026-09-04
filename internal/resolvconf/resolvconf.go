package resolvconf

import (
	"os"
	"strings"
	"sync"
	"time"
)

var fallback = []string{"1.1.1.1", "8.8.8.8"}

type cache struct {
	mu      sync.Mutex
	servers []string
	at      time.Time
}

var c cache

const ttl = 5 * time.Second

func Nameservers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < ttl && len(c.servers) > 0 {
		return append([]string(nil), c.servers...)
	}
	servers := parseFile("/etc/resolv.conf")
	if len(servers) == 0 {
		servers = append([]string(nil), fallback...)
	}
	c.servers = servers
	c.at = time.Now()
	return append([]string(nil), servers...)
}

func parseFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return Parse(string(data))
}

func Parse(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ns := fields[1]
		if strings.Contains(ns, ":") && !strings.Contains(ns, ".") {
			continue // v1: IPv4 only
		}
		if seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	return out
}
