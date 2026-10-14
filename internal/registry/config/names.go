package config

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Name formats. The spec sets none; these keep names safe to use in paths,
// URLs and log lines.
var (
	// objectName is the name of an agent, skill, server, work item type,
	// trigger mapping, identity source or redaction rule, and of an inbound
	// source or shadow run: a DNS label.
	objectName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// toolName is an MCP tool's name.
	toolName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	// versionLabel is a skill version, such as 1.2.0.
	versionLabel = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	// agentToolName is a name agents call a tool by: what every model vendor
	// accepts as a tool's name.
	agentToolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// checkOwner checks an owner: anyone or any team that can be notified. Agents
// and skills need a person (spec Sections 3.1 and 3.2), named by an email
// address.
func checkOwner(owner string, person bool) error {
	switch {
	case owner == "":
		return errors.New("is required")
	case person:
		addr, err := mail.ParseAddress(owner)
		if err != nil || addr.Address != owner || addr.Name != "" {
			return fmt.Errorf("must be a person's email address, got %q", owner)
		}
	case len(owner) > 254 || strings.ContainsFunc(owner, isSpace):
		return fmt.Errorf("must be one word of at most 254 characters, got %q", owner)
	}
	return nil
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

// Hidden reports whether a reader could miss r, or have the text around it
// reordered: characters that are not printable, other than spaces and the
// white space of plain text; format characters (tags, zero-width characters,
// bidi controls); variation selectors; and the other characters Unicode
// says to ignore by default, such as the Hangul fillers. Each can carry text
// that a model reads and a reviewer does not see.
func Hidden(r rune) bool {
	switch {
	case isSpace(r) || unicode.Is(unicode.Zs, r):
		return false
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Variation_Selector, r),
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r):
		return true
	}
	return !unicode.IsPrint(r)
}

// ToolRef names a tool of a registered server, written server.tool. Server
// names hold no dots, so the first dot separates the two.
type ToolRef struct {
	Server string
	Tool   string
}

func (t ToolRef) String() string { return t.Server + "." + t.Tool }

// AgentName returns the name agents call the tool by: server__tool. Tools of
// different servers never share one, and no board tool's name holds "__".
func (t ToolRef) AgentName() string { return t.Server + "__" + t.Tool }

// ParseToolRef parses server.tool.
func ParseToolRef(s string) (ToolRef, error) {
	server, tool, ok := strings.Cut(s, ".")
	if !ok || !objectName.MatchString(server) || !toolName.MatchString(tool) {
		return ToolRef{}, fmt.Errorf("must be server.tool, got %q", s)
	}
	return ToolRef{Server: server, Tool: tool}, nil
}

// SkillRef names one version of a skill, written name@version.
type SkillRef struct {
	Skill   string
	Version string
}

func (s SkillRef) String() string { return s.Skill + "@" + s.Version }

// ParseSkillRef parses name@version.
func ParseSkillRef(s string) (SkillRef, error) {
	skill, version, ok := strings.Cut(s, "@")
	if !ok || !objectName.MatchString(skill) || !versionLabel.MatchString(version) {
		return SkillRef{}, fmt.Errorf("must be skill@version, got %q", s)
	}
	return SkillRef{Skill: skill, Version: version}, nil
}

// hasCredentials reports whether a URL carries a user name or a password.
// Configuration names secrets by reference; it never holds them.
func hasCredentials(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User != nil
}
