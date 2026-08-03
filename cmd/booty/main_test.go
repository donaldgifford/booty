package main

import "testing"

// Flag defaults are the contract with everyone running booty without arguments,
// and with the docs/go-ipxe walkthrough that leans on them. They were retyped
// wholesale when the flag block moved out of cmdServe, so pin them: a slipped
// port or a dropped "./" is silent at compile time and obvious only in the field.
func TestParseServeFlagDefaults(t *testing.T) {
	c, err := parseServeFlags(nil)
	if err != nil {
		t.Fatalf("parseServeFlags(nil): %v", err)
	}

	strs := map[string]struct{ got, want string }{
		"http-addr":           {c.httpAddr, "0.0.0.0:8080"},
		"tftp-addr":           {c.tftpAddr, "0.0.0.0:69"},
		"boot-dir":            {c.bootDir, "./boot"},
		"catalog":             {c.catalogDir, ""},
		"url":                 {c.baseURL, ""},
		"log-format":          {c.logFormat, "text"},
		"templates-dir":       {c.templatesDir, ""},
		"proxydhcp-addr":      {c.proxyDHCPAddr, "0.0.0.0:67"},
		"proxydhcp-binl-addr": {c.proxyDHCPBINLAddr, "0.0.0.0:4011"},
		"server-ip":           {c.serverIP, ""},
		"proxmox-token":       {c.proxmoxToken, ""},
	}
	for name, v := range strs {
		if v.got != v.want {
			t.Errorf("--%s default = %q, want %q", name, v.got, v.want)
		}
	}
	if c.enableProxyDHCP {
		t.Error("--proxydhcp default = true, want false: proxyDHCP answers other people's DHCP traffic and must be opt-in")
	}
}

// TestParseServeFlagsBinds checks that each flag reaches the field it names.
// StringVar makes a copy-paste mix-up — two flags bound to one field — compile
// and pass a defaults test, so set every flag to a distinct value at once.
func TestParseServeFlagsBinds(t *testing.T) {
	c, err := parseServeFlags([]string{
		"--http-addr", "1.1.1.1:1",
		"--tftp-addr", "2.2.2.2:2",
		"--boot-dir", "/b",
		"--catalog", "/c",
		"--url", "http://u:3",
		"--log-format", "json",
		"--templates-dir", "/t",
		"--proxydhcp",
		"--proxydhcp-addr", "4.4.4.4:4",
		"--proxydhcp-binl-addr", "5.5.5.5:5",
		"--server-ip", "6.6.6.6",
		"--proxmox-token", "n:s",
	})
	if err != nil {
		t.Fatalf("parseServeFlags: %v", err)
	}

	binds := map[string]struct{ got, want string }{
		"http-addr":           {c.httpAddr, "1.1.1.1:1"},
		"tftp-addr":           {c.tftpAddr, "2.2.2.2:2"},
		"boot-dir":            {c.bootDir, "/b"},
		"catalog":             {c.catalogDir, "/c"},
		"url":                 {c.baseURL, "http://u:3"},
		"log-format":          {c.logFormat, "json"},
		"templates-dir":       {c.templatesDir, "/t"},
		"proxydhcp-addr":      {c.proxyDHCPAddr, "4.4.4.4:4"},
		"proxydhcp-binl-addr": {c.proxyDHCPBINLAddr, "5.5.5.5:5"},
		"server-ip":           {c.serverIP, "6.6.6.6"},
		"proxmox-token":       {c.proxmoxToken, "n:s"},
	}
	for name, v := range binds {
		if v.got != v.want {
			t.Errorf("--%s = %q, want %q — is it bound to the wrong field?", name, v.got, v.want)
		}
	}
	if !c.enableProxyDHCP {
		t.Error("--proxydhcp = false after being set")
	}
}

// --url is glued to a path and handed to machines that are mid-boot with no way
// to report back. The failure mode this guards is not a crash: an unusable value
// used to start the server, answer 200, and produce scripts every client would
// choke on, so the operator saw a healthy booty and a rack that would not boot.
func TestCheckBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "empty means derive from Host", url: ""},
		{name: "http", url: "http://192.168.1.10:8080"},
		{name: "https", url: "https://boot.example.com"},
		{name: "trailing slash", url: "http://192.168.1.10:8080/"},
		{name: "subpath behind a proxy", url: "https://example.com/booty"},

		// Caught by url.Parse: a colon in the first path segment.
		{name: "no scheme", url: "192.168.1.10:8080", wantErr: true},
		// Caught only by the scheme check — these parse cleanly as relative
		// paths, so a validator that just checked err would wave them through.
		{name: "bare host", url: "boot.example.com", wantErr: true},
		{name: "prose", url: "not a url at all", wantErr: true},
		{name: "scheme but no host", url: "http://", wantErr: true},
		{name: "wrong scheme", url: "tftp://192.168.1.10", wantErr: true},
		{name: "control character", url: "http://192.168.1.10:8080\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBaseURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkBaseURL(%q) = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}
