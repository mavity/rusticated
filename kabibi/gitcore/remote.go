package gitcore

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	transportssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	cryptossh "golang.org/x/crypto/ssh"
)

var httpTokenEnvironmentNames = []string{
	"GIT_HTTP_TOKEN",
	"GITHUB_TOKEN",
	"GH_TOKEN",
	"GITLAB_TOKEN",
	"BITBUCKET_TOKEN",
}

type httpCredential struct {
	username string
	password string
}

type authenticatedRoundTripper struct {
	base http.RoundTripper
	env  map[string]string
}

// NewAuthenticatedHTTPClient creates an HTTP client that adds Git credentials
// from environment tokens, .git-credentials, or .netrc when a request has no
// Authorization header. Credential values are never included in errors.
func NewAuthenticatedHTTPClient(env map[string]string) *http.Client {
	copyEnv := make(map[string]string, len(env))
	for key, value := range env {
		copyEnv[key] = value
	}
	base := http.DefaultTransport
	if base == nil {
		base = &http.Transport{}
	}
	return &http.Client{Transport: authenticatedRoundTripper{base: base, env: copyEnv}}
}

func (t authenticatedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("Git HTTP request has no URL")
	}
	if req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}

	credential, found := t.environmentCredential()
	if !found {
		var err error
		credential, found, err = t.fileCredential(req.URL)
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return t.base.RoundTrip(req)
	}

	authenticated := req.Clone(req.Context())
	authenticated.Header = req.Header.Clone()
	if authenticated.Header == nil {
		authenticated.Header = make(http.Header)
	}
	authenticated.SetBasicAuth(credential.username, credential.password)
	return t.base.RoundTrip(authenticated)
}

func (t authenticatedRoundTripper) environmentCredential() (httpCredential, bool) {
	for _, name := range httpTokenEnvironmentNames {
		if token := strings.TrimSpace(t.env[name]); token != "" {
			return httpCredential{username: "x-access-token", password: token}, true
		}
	}
	return httpCredential{}, false
}

func (t authenticatedRoundTripper) fileCredential(target *url.URL) (httpCredential, bool, error) {
	home := strings.TrimSpace(t.env["HOME"])
	if home == "" {
		return httpCredential{}, false, nil
	}
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return httpCredential{}, false, nil
	}
	if credential, found, err := lookupGitCredentials(filepath.Join(home, ".git-credentials"), host); err != nil || found {
		return credential, found, err
	}
	return lookupNetrc(filepath.Join(home, ".netrc"), target.Host)
}

func lookupGitCredentials(filename, host string) (httpCredential, bool, error) {
	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return httpCredential{}, false, nil
		}
		return httpCredential{}, false, fmt.Errorf("read Git credential file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		credentialURL, err := url.Parse(line)
		if err != nil || credentialURL.Hostname() == "" ||
			!strings.EqualFold(credentialURL.Hostname(), host) || credentialURL.User == nil {
			continue
		}
		password, _ := credentialURL.User.Password()
		return httpCredential{username: credentialURL.User.Username(), password: password}, true, nil
	}
	if err := scanner.Err(); err != nil {
		return httpCredential{}, false, fmt.Errorf("read Git credential file: %w", err)
	}
	return httpCredential{}, false, nil
}

func lookupNetrc(filename, host string) (httpCredential, bool, error) {
	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return httpCredential{}, false, nil
		}
		return httpCredential{}, false, fmt.Errorf("read netrc credentials: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return httpCredential{}, false, fmt.Errorf("read netrc credentials: %w", err)
	}
	tokens := netrcTokens(string(data))
	machine := ""
	var credential httpCredential
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "machine":
			if machine != "" && credentialHostMatches(machine, host) && credential.username != "" {
				return credential, true, nil
			}
			if i+1 >= len(tokens) {
				continue
			}
			machine = tokens[i+1]
			credential = httpCredential{}
			i++
		case "default":
			machine = ""
			credential = httpCredential{}
		case "login", "password":
			if i+1 >= len(tokens) {
				continue
			}
			if tokens[i] == "login" {
				credential.username = tokens[i+1]
			} else {
				credential.password = tokens[i+1]
			}
			i++
		}
	}
	if credentialHostMatches(machine, host) && credential.username != "" {
		return credential, true, nil
	}
	return httpCredential{}, false, nil
}

func credentialHostMatches(machine, target string) bool {
	if strings.EqualFold(machine, target) {
		return true
	}
	if parsed, err := url.Parse("//" + target); err == nil && parsed.Hostname() != "" {
		return strings.EqualFold(machine, parsed.Hostname())
	}
	return false
}

func netrcTokens(input string) []string {
	var tokens []string
	var token strings.Builder
	var quote rune
	escaped, inComment := false, false
	flush := func() {
		if token.Len() > 0 {
			tokens = append(tokens, token.String())
			token.Reset()
		}
	}
	for _, current := range input {
		if inComment {
			if current == '\n' {
				inComment = false
			}
			continue
		}
		if escaped {
			token.WriteRune(current)
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if current == quote {
				quote = 0
			} else {
				token.WriteRune(current)
			}
			continue
		}
		switch {
		case current == '"' || current == '\'':
			quote = current
		case current == '#':
			flush()
			inComment = true
		case current == ' ' || current == '\t' || current == '\r' || current == '\n':
			flush()
		default:
			token.WriteRune(current)
		}
	}
	flush()
	return tokens
}

// NewSSHAuthMethod resolves SSH credentials and host-key verification policy
// from the provided Git environment without changing process-global settings.
func NewSSHAuthMethod(env map[string]string, user string) (transport.AuthMethod, error) {
	if user == "" {
		user = "git"
	}
	home := env["HOME"]
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve SSH home directory: %w", err)
		}
	}

	var identityFile string
	commandArgs := strings.Fields(env["GIT_SSH_COMMAND"])
	insecureHostKey := false
	for i := 0; i < len(commandArgs); i++ {
		switch commandArgs[i] {
		case "-i":
			if i+1 < len(commandArgs) {
				i++
				identityFile = commandArgs[i]
			}
		default:
			if strings.HasPrefix(commandArgs[i], "-i") && len(commandArgs[i]) > 2 {
				identityFile = strings.TrimPrefix(commandArgs[i], "-i")
			}
			if commandArgs[i] == "StrictHostKeyChecking=no" ||
				commandArgs[i] == "-oStrictHostKeyChecking=no" {
				insecureHostKey = true
			}
		}
	}
	if identityFile == "" {
		identityFile = env["GIT_SSH_KEY"]
	}

	var hostKeyCallback cryptossh.HostKeyCallback
	if insecureHostKey {
		hostKeyCallback = cryptossh.InsecureIgnoreHostKey()
	} else {
		files := filepath.SplitList(env["SSH_KNOWN_HOSTS"])
		if len(files) == 0 {
			files = []string{
				filepath.Join(home, ".ssh", "known_hosts"),
				filepath.Join(string(filepath.Separator), "etc", "ssh", "ssh_known_hosts"),
			}
		}
		var err error
		hostKeyCallback, err = transportssh.NewKnownHostsCallback(files...)
		if err != nil {
			return nil, fmt.Errorf("configure SSH host-key verification: %w", err)
		}
	}

	if identityFile != "" {
		if !filepath.IsAbs(identityFile) {
			identityFile = filepath.Join(home, identityFile)
		}
		keys, err := transportssh.NewPublicKeysFromFile(user, identityFile, env["GIT_SSH_KEY_PASSWORD"])
		if err != nil {
			return nil, fmt.Errorf("load SSH identity file: %w", err)
		}
		keys.HostKeyCallback = hostKeyCallback
		return keys, nil
	}
	if env["SSH_AUTH_SOCK"] != "" {
		agent, err := transportssh.NewSSHAgentAuth(user)
		if err != nil {
			return nil, fmt.Errorf("connect to SSH agent: %w", err)
		}
		agent.HostKeyCallback = hostKeyCallback
		return agent, nil
	}

	var lastErr error
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		filename := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(filename); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect SSH identity file: %w", err)
		}
		keys, err := transportssh.NewPublicKeysFromFile(user, filename, env["GIT_SSH_KEY_PASSWORD"])
		if err == nil {
			keys.HostKeyCallback = hostKeyCallback
			return keys, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("load SSH identity file: %w", lastErr)
	}
	return nil, fmt.Errorf("no SSH agent or supported private key found")
}
