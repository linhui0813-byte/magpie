package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// A console session belongs to the CLI. Serialize its renewal and read
// its file again before and after, so concurrent reads share one renewal
// and a profile switched by the user never supplies another card's quota.
var qwenRenewGate = make(chan struct{}, 1)

var qwenRenewLast struct {
	key string
	at  time.Time
	err error
}

var runQwenConsoleCLI = qwenConsoleCLI

var qwenCLIExecutable = func() string {
	if bin := proc.FindTool("bl"); bin != "" {
		return bin
	}
	return proc.FindTool("bailian")
}

func qwenConsoleAllowance(ctx context.Context, c qwenConsole) (qwenConsole, []QuotaWindow, error) {
	ws, err := qwenConsoleWindows(ctx, c)
	if !errors.Is(err, errQwenConsole) || !c.CanRenew {
		return c, ws, err
	}
	select {
	case qwenRenewGate <- struct{}{}:
		defer func() { <-qwenRenewGate }()
	case <-ctx.Done():
		return c, nil, errQwenConsole
	}
	next, err := readQwenConsole()
	if err != nil {
		return next, nil, err
	}
	if !sameQwenScope(c, next) {
		return next, nil, errors.New("Bailian console profile changed; refresh the Qwen Token Plan card")
	}
	if next.Token != c.Token {
		// Another read (or the user) has already renewed this session.
		ws, err = qwenConsoleWindows(ctx, next)
		return next, ws, err
	}
	if !next.CanRenew {
		return next, nil, errQwenConsole
	}
	key := keyTag("qwen-renew", c.Dir+"/"+c.Profile+"/"+c.Token+fmt.Sprint(c.SwitchAgent))
	if qwenRenewLast.key == key && time.Since(qwenRenewLast.at) < time.Minute {
		return next, nil, qwenRenewLast.err
	}
	// An expired session never falls back to an old reading, even when
	// renewal itself fails because the network or the CLI is unavailable.
	renewErr := fmt.Errorf("%w; automatic renewal failed; check the Bailian CLI AccessKey credentials and permissions", errQwenConsole)
	qwenRenewLast.key, qwenRenewLast.at, qwenRenewLast.err = key, time.Now(), renewErr
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	err = runQwenConsoleCLI(rctx, next)
	cancel()
	// The CLI can save a replacement token and then fail its quota retry.
	// That is a lookup failure, not a failed sign-in: read the new session
	// before classifying its exit, without exposing either output stream.
	renewed, readErr := readQwenConsole()
	if readErr != nil {
		return renewed, nil, readErr
	}
	if !sameQwenScope(next, renewed) {
		return renewed, nil, errors.New("Bailian console profile changed during renewal; refresh the Qwen Token Plan card")
	}
	if err != nil && renewed.Token == next.Token {
		return renewed, nil, renewErr
	}
	ws, err = qwenConsoleWindows(ctx, renewed)
	if renewed.Token == next.Token && errors.Is(err, errQwenConsole) {
		return renewed, nil, renewErr
	}
	return renewed, ws, err
}

func sameQwenScope(a, b qwenConsole) bool {
	return a.Dir == b.Dir && a.Profile == b.Profile && a.Region == b.Region && a.Site == b.Site && a.SwitchAgent == b.SwitchAgent
}

// qwenConsoleCLI asks the official CLI for this same read-only allowance.
// Its client renews a refused token from its saved AccessKey pair, retries
// once, and saves the token in its own profile. Neither output stream is
// exposed: the card is read from the gateway with the newly saved session.
func qwenConsoleCLI(ctx context.Context, c qwenConsole) error {
	bin := qwenCLIExecutable()
	if bin == "" {
		return errors.New("Bailian CLI is not installed")
	}
	args := []string{"console", "call", "--api", qwenUsageAPI, "--data", "{}", "--config", c.Profile,
		"--console-region", "cn-beijing", "--console-site", "domestic", "--timeout", "8", "--output", "json", "--quiet"}
	// npm's Windows shim is a .cmd file, not an executable. Start its
	// package with node directly, without placing arguments in a shell.
	if strings.EqualFold(filepath.Ext(bin), ".cmd") {
		entry := filepath.Join(filepath.Dir(bin), "node_modules", "bailian-cli", "dist", "bailian.mjs")
		if st, err := os.Stat(entry); err != nil || st.IsDir() {
			return errors.New("Bailian CLI npm entry is missing")
		}
		bin = proc.FindTool("node")
		if bin == "" {
			return errors.New("Bailian CLI needs node")
		}
		args = append([]string{entry}, args...)
	}
	cmd := proc.CommandContext(ctx, bin, args...)
	cmd.Env = qwenConsoleCLIEnv(os.Environ(), c.Dir, filepath.Dir(bin))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

func qwenConsoleCLIEnv(env []string, dir, binDir string) []string {
	out := make([]string, 0, len(env)+4)
	path := ""
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PATH":
			path = v
		case "BAILIAN_CONFIG_DIR", "DASHSCOPE_BASE_URL", "BAILIAN_BASE_URL", "DASHSCOPE_VERBOSE", "DO_NOT_TRACK":
			continue
		default:
			out = append(out, kv)
		}
	}
	// bl's shebang needs the node beside it when installed with nvm.
	out = append(out, "PATH="+binDir+string(os.PathListSeparator)+path, "BAILIAN_CONFIG_DIR="+dir,
		"DASHSCOPE_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1", "DO_NOT_TRACK=1")
	return out
}
