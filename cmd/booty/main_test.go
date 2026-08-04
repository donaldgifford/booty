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
