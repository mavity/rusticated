package shell

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/mavity/rusticated/kabibi/gitcore"
	"mvdan.cc/sh/v3/interp"
)

func runSSHRemoteBuiltin(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) (bool, error) {
	if len(invocation.args) == 0 {
		return false, nil
	}
	switch invocation.args[0] {
	case "clone":
		remoteURL, destination, bare, branch, depth, singleBranch, ok := parseSSHCloneArgs(invocation)
		if !ok || !isSSHRemoteURL(remoteURL) {
			return false, nil
		}
		fsys, _, err := newHostFS(invocation.effectiveDir)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git clone: %v\n", err)
			return true, interp.ExitStatus(1)
		}
		execSess, _ := execSessionFromContext(ctx)
		env, err := buildGitEnv(execSess, hc, fsys, invocation.config)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git clone: %v\n", err)
			return true, interp.ExitStatus(1)
		}
		auth, err := gitcore.NewSSHAuthMethod(env, sshRemoteUser(remoteURL))
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git clone: %v\n", err)
			return true, interp.ExitStatus(128)
		}
		fmt.Fprintf(hc.Stdout, "Cloning into '%s'...\n", filepath.Base(destination))
		_, err = git.PlainCloneContext(ctx, destination, bare, &git.CloneOptions{
			URL:           remoteURL,
			Auth:          auth,
			ReferenceName: plumbing.ReferenceName(branch),
			SingleBranch:  singleBranch,
			Depth:         depth,
		})
		return true, reportSSHRemoteError(hc.Stderr, "clone", err)

	case "ls-remote":
		remoteURL, headsOnly, tagsOnly, ok := parseSSHListArgs(invocation.args[1:])
		if !ok {
			return false, nil
		}
		if !isSSHRemoteURL(remoteURL) {
			repoPath := invocation.effectiveDir
			if invocation.gitDir != "" {
				repoPath = invocation.gitDir
			}
			repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
			if err != nil {
				return false, nil
			}
			cfg, err := repo.Config()
			if err != nil {
				fmt.Fprintf(hc.Stderr, "git ls-remote: read repository config: %v\n", err)
				return true, interp.ExitStatus(1)
			}
			remote, exists := cfg.Remotes[remoteURL]
			if !exists || len(remote.URLs) == 0 {
				return false, nil
			}
			remoteURL = remote.URLs[0]
		}
		if !isSSHRemoteURL(remoteURL) {
			return false, nil
		}
		auth, err := sshAuthForInvocation(ctx, hc, invocation, remoteURL)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git ls-remote: %v\n", err)
			return true, interp.ExitStatus(128)
		}
		remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
			Name: "origin",
			URLs: []string{remoteURL},
		})
		refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
		if err != nil {
			return true, reportSSHRemoteError(hc.Stderr, "ls-remote", err)
		}
		for _, ref := range refs {
			if headsOnly || tagsOnly {
				isHead := strings.HasPrefix(string(ref.Name()), "refs/heads/")
				isTag := strings.HasPrefix(string(ref.Name()), "refs/tags/")
				if !(headsOnly && isHead) && !(tagsOnly && isTag) {
					continue
				}
			}
			fmt.Fprintf(hc.Stdout, "%s\t%s\n", ref.Hash(), ref.Name())
		}
		return true, nil

	case "fetch", "pull", "push":
		return runSSHRepositoryRemote(ctx, hc, invocation)
	default:
		return false, nil
	}
}

func runSSHRepositoryRemote(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) (bool, error) {
	args := invocation.args[1:]
	remoteName := "origin"
	remoteSelected := false
	remoteURL := ""
	pullBranch := ""
	var refspecs []config.RefSpec
	force := false
	for _, arg := range args {
		switch {
		case arg == "--force" || arg == "-f":
			force = true
		case strings.HasPrefix(arg, "-"):
		case isSSHRemoteURL(arg):
			remoteURL = arg
			remoteSelected = true
		case strings.Contains(arg, ":"):
			refspecs = append(refspecs, config.RefSpec(arg))
		case !remoteSelected:
			remoteName = arg
			remoteSelected = true
		default:
			if invocation.args[0] == "pull" {
				pullBranch = arg
			} else {
				refspecs = append(refspecs, config.RefSpec(arg))
			}
		}
	}
	repoPath := invocation.effectiveDir
	if invocation.gitDir != "" {
		repoPath = invocation.gitDir
	}
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git %s: %v\n", invocation.args[0], err)
		return true, interp.ExitStatus(128)
	}
	if remoteURL == "" {
		cfg, err := repo.Config()
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git %s: read repository config: %v\n", invocation.args[0], err)
			return true, interp.ExitStatus(1)
		}
		remote, ok := cfg.Remotes[remoteName]
		if !ok || len(remote.URLs) == 0 {
			return false, nil
		}
		remoteURL = remote.URLs[0]
	}
	if !isSSHRemoteURL(remoteURL) {
		return false, nil
	}
	auth, err := sshAuthForInvocation(ctx, hc, invocation, remoteURL)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git %s: %v\n", invocation.args[0], err)
		return true, interp.ExitStatus(128)
	}

	switch invocation.args[0] {
	case "fetch":
		err = repo.FetchContext(ctx, &git.FetchOptions{
			RemoteName: remoteName,
			RemoteURL:  remoteURL,
			RefSpecs:   refspecs,
			Force:      force,
			Auth:       auth,
		})
	case "pull":
		var worktree *git.Worktree
		worktree, err = repo.Worktree()
		if err == nil {
			err = worktree.PullContext(ctx, &git.PullOptions{
				RemoteName:    remoteName,
				RemoteURL:     remoteURL,
				ReferenceName: plumbing.ReferenceName(pullBranch),
				SingleBranch:  pullBranch != "",
				Auth:          auth,
			})
		}
	case "push":
		err = repo.PushContext(ctx, &git.PushOptions{
			RemoteName: remoteName,
			RemoteURL:  remoteURL,
			RefSpecs:   refspecs,
			Force:      force,
			Auth:       auth,
		})
	}
	return true, reportSSHRemoteError(hc.Stderr, invocation.args[0], err)
}

func sshAuthForInvocation(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation, remoteURL string) (transport.AuthMethod, error) {
	fsys, _, err := newHostFS(invocation.effectiveDir)
	if err != nil {
		return nil, err
	}
	execSess, _ := execSessionFromContext(ctx)
	env, err := buildGitEnv(execSess, hc, fsys, invocation.config)
	if err != nil {
		return nil, err
	}
	return gitcore.NewSSHAuthMethod(env, sshRemoteUser(remoteURL))
}

func isSSHRemoteURL(remoteURL string) bool {
	endpoint, err := transport.NewEndpoint(remoteURL)
	return err == nil && endpoint.Protocol == "ssh"
}

func sshRemoteUser(remoteURL string) string {
	endpoint, err := transport.NewEndpoint(remoteURL)
	if err != nil {
		return ""
	}
	return endpoint.User
}

func parseSSHCloneArgs(invocation gitInvocation) (remoteURL, destination string, bare bool, branch string, depth int, singleBranch, ok bool) {
	args := invocation.args[1:]
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--bare":
			bare = true
		case "--single-branch":
			singleBranch = true
		case "-b", "--branch":
			if i+1 >= len(args) {
				return "", "", false, "", 0, false, false
			}
			i++
			branch = args[i]
		case "--depth":
			if i+1 >= len(args) {
				return "", "", false, "", 0, false, false
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value < 1 {
				return "", "", false, "", 0, false, false
			}
			depth = value
		case "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", false, "", 0, false, false
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) < 1 || len(positional) > 2 {
		return "", "", false, "", 0, false, false
	}
	remoteURL = positional[0]
	if len(positional) == 2 {
		destination = resolveShellPath(invocation.effectiveDir, positional[1])
	} else {
		endpoint, err := transport.NewEndpoint(remoteURL)
		if err != nil {
			return "", "", false, "", 0, false, false
		}
		name := filepath.Base(strings.TrimSuffix(endpoint.Path, "/"))
		name = strings.TrimSuffix(name, ".git")
		if name == "." || name == string(filepath.Separator) || name == "" {
			return "", "", false, "", 0, false, false
		}
		destination = filepath.Join(invocation.effectiveDir, name)
	}
	return remoteURL, destination, bare, branch, depth, singleBranch, true
}

func parseSSHListArgs(args []string) (remoteURL string, headsOnly, tagsOnly, ok bool) {
	for _, arg := range args {
		switch arg {
		case "--heads", "-h":
			headsOnly = true
		case "--tags", "-t":
			tagsOnly = true
		case "--":
		default:
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if remoteURL != "" {
				return "", false, false, false
			}
			remoteURL = arg
		}
	}
	return remoteURL, headsOnly, tagsOnly, remoteURL != ""
}

func reportSSHRemoteError(stderr io.Writer, command string, err error) error {
	if err == nil || err == git.NoErrAlreadyUpToDate {
		return nil
	}
	fmt.Fprintf(stderr, "git %s: %v\n", command, err)
	return interp.ExitStatus(1)
}
