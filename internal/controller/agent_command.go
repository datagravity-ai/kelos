package controller

const (
	tiniPath = "/usr/bin/tini"

	// agentProcessScript keeps custom images that do not yet include Tini
	// compatible while ensuring the bundled images use Tini as PID 1.
	agentProcessScript = `if [ -x ` + tiniPath + ` ]; then exec ` + tiniPath + ` -g -- "$@"; fi; exec "$@"`
)

func agentProcessCommand(program string) []string {
	return []string{"/bin/sh", "-c", agentProcessScript, "--", program}
}
