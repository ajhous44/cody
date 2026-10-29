package cli

import (
	"fmt"
	"strconv"
	"strings"
)

type options struct {
	command      string
	prompt       []string
	provider     string
	model        string
	approval     string
	resume       string
	addr         string
	token        string
	jsonOut      bool
	list         bool
	keep         bool
	open         bool
	watchStdin   bool
	showThinking bool
	plan         bool
	sandbox      string
	rebuild      bool
	runs         int
	task         string
	allowHome    bool
	debug        *bool
	forkFrom     string // session to fork ("last" by default)
	forkAt       int    // user turn to fork before; 0 copies the whole session
	rerun        bool   // re-send that turn in the fork
}

var commands = map[string]bool{
	"run": true, "sessions": true, "resume": true, "replay": true, "fork": true,
	"bench": true, "models": true, "index": true, "serve": true,
	"help": true, "version": true,
}

func parseArgs(args []string) (options, error) {
	o := options{runs: 1}
	for i := 0; i < len(args); i++ {
		a := args[i]
		need := func(flag string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", flag)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--":
			o.prompt = append(o.prompt, args[i+1:]...)
			return normalize(o), nil
		case a == "-h" || a == "--help":
			o.command = "help"
		case a == "-v" || a == "--version":
			o.command = "version"
		case a == "--json":
			o.jsonOut = true
		case a == "--list":
			o.list = true
		case a == "--keep":
			o.keep = true
		case a == "--open":
			o.open = true
		case a == "--watch-stdin":
			o.watchStdin = true
		case a == "--yolo":
			o.approval = "yolo"
		case a == "--allow-home":
			o.allowHome = true
		case a == "--show-thinking":
			o.showThinking = true
		case a == "--plan":
			o.plan = true
		case a == "--sandbox":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.sandbox = v
		case a == "--rerun":
			o.rerun = true
		case a == "--at":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return o, fmt.Errorf("--at must be a turn number (1 or more)")
			}
			o.forkAt = n
		case a == "--rebuild":
			o.rebuild = true
		case a == "--debug" || strings.HasPrefix(a, "--debug="):
			v := true
			if s, ok := strings.CutPrefix(a, "--debug="); ok {
				var err error
				if v, err = strconv.ParseBool(s); err != nil {
					return o, fmt.Errorf("--debug must be true or false")
				}
			}
			o.debug = &v
		case a == "-m" || a == "--model":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.model = v
		case a == "-p" || a == "--provider":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.provider = v
		case a == "--approval":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.approval = v
		case a == "--resume":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.resume = v
		case a == "--addr":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.addr = v
		case a == "--token":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.token = v
		case a == "--task":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			o.task = v
		case a == "--runs":
			v, err := need(a)
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return o, fmt.Errorf("--runs must be a positive integer")
			}
			o.runs = n
		case strings.HasPrefix(a, "--"):
			return o, fmt.Errorf("unknown flag %s", a)
		default:
			if o.command == "" && commands[a] {
				o.command = a
			} else {
				o.prompt = append(o.prompt, a)
			}
		}
	}
	return normalize(o), nil
}

// normalize applies command-specific positional handling.
func normalize(o options) options {
	switch o.command {
	case "resume":
		if o.resume == "" {
			if len(o.prompt) > 0 {
				o.resume = o.prompt[0]
				o.prompt = o.prompt[1:]
			} else {
				o.resume = "last"
			}
		}
	case "replay":
		if len(o.prompt) == 0 {
			o.prompt = []string{"last"}
		}
	case "fork":
		o.forkFrom = "last"
		if len(o.prompt) > 0 {
			o.forkFrom, o.prompt = o.prompt[0], o.prompt[1:]
		}
	}
	return o
}
