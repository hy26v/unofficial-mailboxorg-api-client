// ABOUTME: Cross-platform user configuration and credential discovery for mbx.
// ABOUTME: Keeps durable settings in ~/.mbx/config.toml while leaving passwords ephemeral.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/fsutil"
	"github.com/pelletier/go-toml/v2"
)

const (
	defaultDirectoryName = ".mbx"
	defaultConfigName    = "config.toml"
	defaultSessionName   = "session.json"
	maxConfigSize        = 1 << 20
	maxUsernameLength    = 254
)

// Paths contains the user-level configuration and state locations used by mbx.
type Paths struct {
	Root        string
	ConfigFile  string
	SessionFile string
}

// ResolvePaths returns the mbx user directory and its default files. MBX_HOME is an
// optional relocation mechanism for automation; normal use resolves the current user's
// home directory through the operating system.
func ResolvePaths(getenv func(string) string, userHomeDir func() (string, error)) (Paths, error) {
	root := ""
	if getenv != nil {
		root = strings.TrimSpace(getenv("MBX_HOME"))
	}
	if root == "" {
		if userHomeDir == nil {
			return Paths{}, fmt.Errorf("could not determine the user home directory")
		}
		home, err := userHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("could not determine the user home directory: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return Paths{}, fmt.Errorf("could not determine the user home directory: empty path")
		}
		root = filepath.Join(home, defaultDirectoryName)
	} else if !filepath.IsAbs(root) {
		return Paths{}, fmt.Errorf("MBX_HOME must be an absolute path")
	}

	if strings.ContainsRune(root, '\x00') {
		return Paths{}, fmt.Errorf("mbx home directory contains a NUL byte")
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return Paths{}, fmt.Errorf("mbx home directory must be an absolute path")
	}
	return Paths{
		Root:        root,
		ConfigFile:  filepath.Join(root, defaultConfigName),
		SessionFile: filepath.Join(root, defaultSessionName),
	}, nil
}

// Settings is the durable, non-secret configuration read from config.toml.
type Settings struct {
	Username string         `toml:"username,omitempty"`
	Safety   SafetySettings `toml:"safety,omitempty"`
	State    StateSettings  `toml:"state,omitempty"`
	MCP      MCPSettings    `toml:"mcp,omitempty"`
}

// SafetySettings controls local safeguards around destructive operations.
type SafetySettings struct {
	DeleteProtection []string `toml:"delete_protection,omitempty"`
	// ReadOnly refuses every command and flag that changes account data.
	ReadOnly bool `toml:"read_only,omitempty"`
}

// MCPSettings limits what "mbx mcp" offers to MCP clients.
type MCPSettings struct {
	// AllowedWrites, when set, names the only operations that change data that MCP
	// offers. Nil offers all of them; an empty list offers none.
	AllowedWrites *[]string `toml:"allowed_writes,omitempty"`
}

// StateSettings controls where mutable local state is stored.
type StateSettings struct {
	SessionFile string `toml:"session_file,omitempty"`
}

// Load reads a strict TOML settings file. A missing file is equivalent to an empty
// configuration. Unknown fields are rejected so misspelled safety keys never silently
// disable protection.
func Load(path string) (Settings, error) {
	data, err := readConfigFile(path)
	if err != nil || data == nil {
		return Settings{}, err
	}
	return decodeSettings(path, data)
}

// readConfigFile returns the raw config text, or nil without an error when the file does
// not exist. It follows symbolic links, like the rest of config loading.
func readConfigFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not inspect config file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config path is not a regular file: %s", path)
	}
	if info.Size() > maxConfigSize {
		return nil, fmt.Errorf("config file exceeds the %d-byte limit: %s", maxConfigSize, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read config file %s: %w", path, err)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func decodeSettings(path string, data []byte) (Settings, error) {
	var settings Settings
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		var unknown *toml.StrictMissingError
		if errors.As(err, &unknown) {
			keys := make([]string, 0, len(unknown.Errors))
			for _, decodeErr := range unknown.Errors {
				keys = append(keys, strings.Join(decodeErr.Key(), "."))
			}
			return Settings{}, fmt.Errorf(
				"invalid config file %s: unknown field(s): %s",
				path,
				strings.Join(keys, ", "),
			)
		}
		return Settings{}, fmt.Errorf("invalid config file %s: %w", path, err)
	}
	settings.Username = strings.TrimSpace(settings.Username)
	settings.State.SessionFile = strings.TrimSpace(settings.State.SessionFile)
	return settings, nil
}

// SaveUsername validates and atomically adds a prompted mailbox address to config.toml.
// Only the username key changes: the rest of the file text, including comments, key
// order and quoting, is kept. The edited text is decoded again and must give the same
// safety and state settings, so an edit can never weaken them. When config.toml is a
// symbolic link, the file it points to is updated and the link stays in place. A
// username already present when saving is never overwritten with a different value.
func SaveUsername(path, value string) (string, error) {
	username, err := NormalizeUsername(value)
	if err != nil {
		return "", err
	}

	target, err := resolveConfigTarget(path)
	if err != nil {
		return "", err
	}
	data, err := readConfigFile(target)
	if err != nil {
		return "", err
	}
	settings, err := decodeSettings(target, data)
	if err != nil {
		return "", err
	}
	if settings.Username != "" {
		if settings.Username == username {
			return username, nil
		}
		return "", fmt.Errorf("config already contains a different username; refusing to overwrite it")
	}

	updated, err := setTopLevelUsername(data, username)
	if err != nil {
		return "", fmt.Errorf("could not update config file %s: %w", path, err)
	}
	if len(updated) > maxConfigSize {
		return "", fmt.Errorf("encoded config exceeds the %d-byte limit: %s", maxConfigSize, path)
	}
	check, err := decodeSettings(target, updated)
	if err != nil || check.Username != username || !reflect.DeepEqual(check.Safety, settings.Safety) || check.State != settings.State {
		return "", fmt.Errorf("could not add the username to config file %s safely; add username = %q to it by hand", path, username)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("could not write config file %s: %w", path, err)
	}
	if err := fsutil.WriteFileAtomic(target, updated, 0o600); err != nil {
		return "", fmt.Errorf("could not write config file %s: %w", path, err)
	}
	return username, nil
}

// resolveConfigTarget returns the file that holds the configuration: path itself, or the
// final target when path is a symbolic link. Writing to the target keeps a link (for
// example into a dotfiles repository) in place instead of replacing it with a copy.
func resolveConfigTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("could not inspect config file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("config file %s is a symbolic link that cannot be followed: %w", path, err)
	}
	return target, nil
}

var (
	tomlTableHeader   = regexp.MustCompile(`^\s*\[`)
	tomlUsernameKey   = regexp.MustCompile(`^\s*(?:username|"username"|'username')\s*=`)
	tomlEmptyUsername = regexp.MustCompile(`^(\s*(?:username|"username"|'username')\s*=\s*)(?:"\s*"|'\s*')(.*)$`)
)

// setTopLevelUsername returns data with a top-level username key set to username. An
// existing blank username key is filled in place, keeping a comment on that line.
// Otherwise a new line is added before the first table, above any comment lines that
// belong to that table, or at the end of a file without tables. Line endings follow the
// file. SaveUsername verifies the result by decoding it.
func setTopLevelUsername(data []byte, username string) ([]byte, error) {
	encoded, err := toml.Marshal(struct {
		Username string `toml:"username"`
	}{username})
	if err != nil {
		return nil, err
	}
	keyLine := strings.TrimRight(string(encoded), "\r\n")
	value := strings.TrimSpace(strings.SplitN(keyLine, "=", 2)[1])

	text := string(data)
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	firstTable := len(lines)
	for i, line := range lines {
		if tomlTableHeader.MatchString(line) {
			firstTable = i
			break
		}
	}
	for i := 0; i < firstTable; i++ {
		if !tomlUsernameKey.MatchString(lines[i]) {
			continue
		}
		body := strings.TrimRight(lines[i], "\r\n")
		ending := lines[i][len(body):]
		match := tomlEmptyUsername.FindStringSubmatch(body)
		if match == nil {
			return nil, fmt.Errorf("the existing username entry has an unexpected form")
		}
		lines[i] = match[1] + value + match[2] + ending
		return []byte(strings.Join(lines, "")), nil
	}

	if firstTable == len(lines) {
		if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
			lines[len(lines)-1] += newline
		}
		lines = append(lines, keyLine+newline)
		return []byte(strings.Join(lines, "")), nil
	}
	insertAt := firstTable
	for insertAt > 0 && strings.HasPrefix(strings.TrimSpace(lines[insertAt-1]), "#") {
		insertAt--
	}
	addition := []string{keyLine + newline, newline}
	lines = append(lines[:insertAt], append(addition, lines[insertAt:]...)...)
	return []byte(strings.Join(lines, "")), nil
}

// NormalizeUsername validates a mailbox address collected by interactive setup.
func NormalizeUsername(value string) (string, error) {
	username := strings.TrimSpace(value)
	if username == "" {
		return "", fmt.Errorf("mailbox.org email cannot be empty")
	}
	if len(username) > maxUsernameLength {
		return "", fmt.Errorf("mailbox.org email exceeds %d bytes", maxUsernameLength)
	}
	if strings.IndexFunc(username, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("mailbox.org email cannot contain whitespace or control characters")
	}
	if strings.Count(username, "@") != 1 {
		return "", fmt.Errorf("mailbox.org email must contain one @ between a local part and domain")
	}
	local, domain, _ := strings.Cut(username, "@")
	if local == "" || domain == "" || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", fmt.Errorf("mailbox.org email must contain a non-empty local part and domain")
	}
	return username, nil
}

// ResolveSessionFile resolves a configured session path. Relative paths are anchored at
// the directory containing config.toml, not at the caller's current working directory.
func (s Settings) ResolveSessionFile(configPath, defaultPath string) (string, error) {
	configured := s.State.SessionFile
	if configured == "" {
		return defaultPath, nil
	}
	if strings.ContainsRune(configured, '\x00') {
		return "", fmt.Errorf("state.session_file contains a NUL byte")
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured), nil
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configPath), configured)), nil
}

// Environment overlays durable non-secret settings onto the process environment used
// by legacy command handlers. Passwords are intentionally never read from config.toml.
func (s Settings) Environment(getenv func(string) string) func(string) string {
	return func(key string) string {
		if key == "MBX_USERNAME" && s.Username != "" {
			return s.Username
		}
		if getenv == nil {
			return ""
		}
		return getenv(key)
	}
}

// Credentials holds a mailbox.org username and password.
type Credentials struct {
	Username string
	Password string
}

// String implements fmt.Stringer, redacting the password.
func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{Username: %s, Password: [REDACTED]}", c.Username)
}

// GoString implements fmt.GoStringer, redacting the password from %#v output too.
func (c Credentials) GoString() string {
	return c.String()
}

// LoadAuthenticationCredentials reads the effective username and an optional ephemeral
// password for session-aware commands. The password may be empty because Client.Authenticate
// first tries the persisted cookie session and needs credentials only as a fallback.
func LoadAuthenticationCredentials(getenv func(string) string) (Credentials, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	username := strings.TrimSpace(getenv("MBX_USERNAME"))
	password := getenv("MBX_PASSWORD")

	if username == "" && password == "" {
		return Credentials{}, fmt.Errorf("missing credentials: configure username in config.toml (see \"mbx help config\") and set MBX_PASSWORD for login")
	}
	if username == "" {
		return Credentials{}, fmt.Errorf("missing credentials: configure username in config.toml (see \"mbx help config\")")
	}

	return Credentials{Username: username, Password: password}, nil
}

// LoadCredentials requires both the effective username and ephemeral password for an
// explicit credential operation such as login. Passwords are never accepted from the
// ordinary config file.
func LoadCredentials(getenv func(string) string) (Credentials, error) {
	credentials, err := LoadAuthenticationCredentials(getenv)
	if err != nil {
		return Credentials{}, err
	}
	if credentials.Password == "" {
		return Credentials{}, fmt.Errorf("missing credentials: set MBX_PASSWORD (passwords are not stored in config.toml)")
	}
	return credentials, nil
}
