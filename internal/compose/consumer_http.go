package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/system"
)

const (
	consumerKeyEnv       = "SELFHOST_PEER_API_KEY"
	consumerBodyLimit    = 1 << 20
	consumerBudget       = 8 * time.Second
	consumerTransferNeed = 5 * time.Second
	consumerPipeReap     = time.Second
	consumerMinCurl      = 8<<16 | 4<<8
	consumerNetworkLimit = 64
	consumerProbeOutput  = 4 << 10
	consumerInspectLimit = 64 << 10
	consumerStderrLimit  = 4 << 10
)

var (
	consumerService = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	consumerRoute   = regexp.MustCompile(`^/[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$`)
	consumerID      = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	consumerTrailer = regexp.MustCompile(`^\n--selfhost-v1 ([0-9]{3}) ([0-9.]{7,15}) ([0-9]{1,5})\n$`)
	consumerCurl    = regexp.MustCompile(`^curl ([0-9]+)\.([0-9]+)\.([0-9]+)`)
)

func refused(reason string) error {
	return fmt.Errorf("%w: consumer request refused: %s", errors.ErrUnsupported, reason)
}

var errConsumerTransfer = errors.New("consumer request did not complete")

type sink struct {
	buf    []byte
	limit  int
	over   bool
	cancel context.CancelFunc
}

func (s *sink) Write(p []byte) (int, error) {
	written := len(p)
	room := s.limit - len(s.buf)
	if written > room {
		s.over = true
		s.cancel()
		p = p[:room]
	}
	s.buf = append(s.buf, p...)
	return written, nil
}

type endpoint struct {
	id, image, network, networkID, ip, aliases string
}

func (c *Compose) consumerRun(ctx context.Context, env []string, limit int, args ...string) ([]byte, error) {
	runner, ok := c.deps.Runner.(system.StreamIORunner)
	if !ok {
		return nil, refused("the runner cannot bound command output")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &sink{limit: limit, cancel: cancel}
	stderr := &sink{limit: consumerStderrLimit, cancel: cancel}
	cmd := system.Cmd{Name: "docker", Args: args, Dir: c.root, Env: env, WaitDelay: consumerPipeReap}
	c.log.Emit(events.Event{Event: events.Command, Command: "docker " + strings.Join(args, " ")})
	err := runner.StreamIO(ctx, cmd, nil, stdout, stderr)
	if stdout.over || stderr.over {
		return nil, errConsumerTransfer
	}
	if err != nil {
		if parent := ctx.Err(); parent != nil {
			return nil, parent
		}
		return nil, errConsumerTransfer
	}
	return stdout.buf, nil
}

func (c *Compose) projectID(ctx context.Context, service string) (string, error) {
	result, err := c.Run(ctx, nil, "ps", "-q", service)
	if err != nil {
		return "", refused("the Project service could not be listed")
	}
	ids := strings.Fields(string(result.Stdout))
	if len(ids) != 1 || !consumerID.MatchString(ids[0]) {
		return "", refused("the service does not have exactly one running container")
	}
	return ids[0], nil
}

func (c *Compose) endpoint(ctx context.Context, service string) (endpoint, error) {
	var found endpoint
	id, err := c.projectID(ctx, service)
	if err != nil {
		return found, err
	}
	const projection = `{"id":{{json .Id}},"image":{{json .Image}},"running":{{json .State.Running}},"labels":{{json .Config.Labels}},"mode":{{json .HostConfig.NetworkMode}},"networks":{{json .NetworkSettings.Networks}}}`
	raw, err := c.consumerRun(ctx, nil, consumerInspectLimit, "container", "inspect", "--format", projection, id)
	if err != nil {
		return found, err
	}
	var container struct {
		ID       string            `json:"id"`
		Image    string            `json:"image"`
		Running  bool              `json:"running"`
		Labels   map[string]string `json:"labels"`
		Mode     string            `json:"mode"`
		Networks map[string]struct {
			NetworkID         string   `json:"NetworkID"`
			IPAddress         string   `json:"IPAddress"`
			GlobalIPv6Address string   `json:"GlobalIPv6Address"`
			Aliases           []string `json:"Aliases"`
		} `json:"networks"`
	}
	if json.Unmarshal(raw, &container) != nil || container.ID != id || !container.Running || container.Image == "" {
		return found, refused("the container identity could not be verified")
	}
	labels := container.Labels
	if labels["com.docker.compose.project"] != c.name || labels["com.docker.compose.service"] != service || labels["com.docker.compose.oneoff"] != "False" || labels["com.docker.compose.container-number"] != "1" {
		return found, refused("the container is not this Project's single service replica")
	}
	if len(container.Networks) != 1 {
		return found, refused("the container is not on exactly one network")
	}
	for name, network := range container.Networks {
		address, err := netip.ParseAddr(network.IPAddress)
		if container.Mode != name || err != nil || !address.Is4() || network.GlobalIPv6Address != "" || network.NetworkID == "" {
			return found, refused("the container network is not a plain IPv4 user-defined bridge")
		}
		aliases := slices.Clone(network.Aliases)
		slices.Sort(aliases)
		found = endpoint{id: id, image: container.Image, network: name, networkID: network.NetworkID, ip: address.String(), aliases: strings.Join(aliases, ",")}
		if !slices.Contains(aliases, service) {
			return found, refused("the container is not addressable by its service name")
		}
	}
	return found, nil
}

func (c *Compose) bridge(ctx context.Context, networkID string) error {
	const projection = `{"driver":{{json .Driver}},"scope":{{json .Scope}},"ipv6":{{json .EnableIPv6}},"labels":{{json .Labels}},"attached":{{len .Containers}}}`
	raw, err := c.consumerRun(ctx, nil, consumerInspectLimit, "network", "inspect", "--format", projection, networkID)
	if err != nil {
		return err
	}
	var network struct {
		Driver   string            `json:"driver"`
		Scope    string            `json:"scope"`
		IPv6     bool              `json:"ipv6"`
		Labels   map[string]string `json:"labels"`
		Attached int               `json:"attached"`
	}
	if json.Unmarshal(raw, &network) != nil || network.Driver != "bridge" || network.Scope != "local" || network.IPv6 || network.Labels["com.docker.compose.project"] != c.name || network.Attached > consumerNetworkLimit {
		return refused("the shared network is not a bounded Project-owned IPv4 bridge")
	}
	return nil
}

func (c *Compose) inContainer(ctx context.Context, id string, env []string, limit int, command ...string) ([]byte, error) {
	args := []string{"exec", "-T"}
	if len(env) > 0 {
		args = append(args, "-e", consumerKeyEnv)
	}
	args = append(append(args, id, "s6-setuidgid", "abc"), command...)
	return c.consumerRun(ctx, env, limit, args...)
}

func (c *Compose) probeConsumer(ctx context.Context, id string) error {
	raw, err := c.inContainer(ctx, id, nil, consumerProbeOutput, "sh", "-eu", "-c", `id -u; timeout --version | head -n 1; curl --version | grep -E '^(curl|Protocols:)'`)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 || lines[0] == "0" || !strings.Contains(lines[1], "GNU") || !slices.Contains(strings.Fields(lines[3]), "http") {
		return refused("the consumer image lacks the required curl, timeout or application identity")
	}
	version := consumerCurl.FindStringSubmatch(lines[2])
	if version == nil {
		return refused("the consumer curl version could not be verified")
	}
	number := 0
	for _, part := range version[1:] {
		value, _ := strconv.Atoi(part)
		number = number<<8 | value
	}
	if number < consumerMinCurl {
		return refused("the consumer curl version is too old")
	}
	return nil
}

func (c *Compose) resolves(ctx context.Context, id, service string, port int, ip string) error {
	const script = `import socket,sys;print("\n".join(sorted({i[4][0] for i in socket.getaddrinfo(sys.argv[1],int(sys.argv[2]),type=socket.SOCK_STREAM)})))`
	raw, err := c.inContainer(ctx, id, nil, consumerProbeOutput, "timeout", "-k", "1", "3", "python3", "-I", "-B", "-c", script, service, strconv.Itoa(port))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != ip {
		return refused("the consumer's name resolution does not identify exactly the selected target")
	}
	return nil
}

func validConsumerRequest(consumerName, target string, port int, route, key string) error {
	if !consumerService.MatchString(consumerName) || !consumerService.MatchString(target) || port < 1 || port > 65535 || !consumerRoute.MatchString(route) || slices.Contains(strings.Split(route, "/"), "..") || slices.Contains(strings.Split(route, "/"), ".") {
		return refused("the service, port or route is invalid")
	}
	if key == "" || len(key) > 512 {
		return refused("the credential is empty or oversized")
	}
	for _, b := range []byte(key) {
		if b < 0x21 || b > 0x7e {
			return refused("the credential contains unsupported characters")
		}
	}
	return nil
}

func parseConsumerOutput(out []byte, address string, port int) (int, []byte, error) {
	index := bytes.LastIndex(out, []byte("\n--selfhost-v1 "))
	if index < 0 {
		return 0, nil, errConsumerTransfer
	}
	match := consumerTrailer.FindSubmatch(out[index:])
	if match == nil {
		return 0, nil, errConsumerTransfer
	}
	status, _ := strconv.Atoi(string(match[1]))
	remote, _ := strconv.Atoi(string(match[3]))
	body := out[:index]
	if status < 100 || string(match[2]) != address || remote != port || len(body) > consumerBodyLimit {
		return 0, nil, errConsumerTransfer
	}
	return status, body, nil
}

// ConsumerGET reads one literal route of an installed peer from inside the
// consuming service's own container. It runs only in the supported trusted
// bridge-network layout and never hands the key over before admission.
func (c *Compose) ConsumerGET(ctx context.Context, consumerName, targetService string, targetPort int, route, apiKey string) (int, []byte, error) {
	if err := validConsumerRequest(consumerName, targetService, targetPort, route, apiKey); err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, consumerBudget)
	defer cancel()
	consumer, err := c.endpoint(ctx, consumerName)
	if err != nil {
		return 0, nil, err
	}
	target, err := c.endpoint(ctx, targetService)
	if err != nil {
		return 0, nil, err
	}
	if consumer.networkID != target.networkID || consumer.id == target.id {
		return 0, nil, refused("the consumer and target do not share exactly one Project network")
	}
	if err := c.bridge(ctx, consumer.networkID); err != nil {
		return 0, nil, err
	}
	if err := c.probeConsumer(ctx, consumer.id); err != nil {
		return 0, nil, err
	}
	if err := c.resolves(ctx, consumer.id, targetService, targetPort, target.ip); err != nil {
		return 0, nil, err
	}
	if deadline, _ := ctx.Deadline(); time.Until(deadline) < consumerTransferNeed+consumerPipeReap {
		return 0, nil, refused("not enough time remains to bound the transfer")
	}
	origin := targetService + ":" + strconv.Itoa(targetPort)
	out, err := c.inContainer(ctx, consumer.id, []string{consumerKeyEnv + "=" + apiKey}, consumerBodyLimit+128,
		"timeout", "-k", "1", "4", "curl", "-q", "--silent", "--url", "http://"+origin+route,
		"--request", "GET", "--globoff", "--proto", "=http", "--max-redirs", "0",
		"--proxy", "", "--noproxy", "*", "--resolve", origin+":"+target.ip,
		"--connect-timeout", "1", "--max-time", "3", "--max-filesize", strconv.Itoa(consumerBodyLimit+1),
		"--header", "Accept: application/json", "--variable", "%"+consumerKeyEnv,
		"--expand-header", "X-Api-Key: {{"+consumerKeyEnv+"}}",
		"--write-out", `\n--selfhost-v1 %{http_code} %{remote_ip} %{remote_port}\n`)
	if err != nil {
		return 0, nil, err
	}
	status, body, err := parseConsumerOutput(out, target.ip, targetPort)
	if err != nil {
		return 0, nil, err
	}
	after, err := c.endpoint(ctx, targetService)
	if err != nil || after != target {
		return 0, nil, refused("the target endpoint changed during the request")
	}
	again, err := c.endpoint(ctx, consumerName)
	if err != nil || again != consumer {
		return 0, nil, refused("the consumer container changed during the request")
	}
	return status, body, nil
}
