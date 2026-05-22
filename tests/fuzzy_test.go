package tests

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/v2"
	"github.com/pippellia-btc/rely/v2/tests/swarm"
	"github.com/pippellia-btc/rely/v2/utils"
)

// FuzzyConfig holds the configuration for the fuzzy test.
type FuzzyConfig struct {
	Address                        string
	TestDuration                   time.Duration
	RelayDuration                  time.Duration
	SwarmDuration                  time.Duration
	FailProbability                float32
	DisconnectProbability          float32
	SubscriptionClosureProbability float32
	Swarm                          swarm.Config
}

func defaultFuzzyConfig() FuzzyConfig {
	d := 500 * time.Second
	return FuzzyConfig{
		Address:                        "localhost:3334",
		TestDuration:                   d,
		RelayDuration:                  d - 10*time.Second,
		SwarmDuration:                  d - 20*time.Second,
		FailProbability:                0.001,
		DisconnectProbability:          0.001,
		SubscriptionClosureProbability: 0.0001,
		Swarm: swarm.Config{
			ConnectionFrequency: time.Millisecond,
			Client:              swarm.DefaultClientConfig(),
		},
	}
}

// TestFuzzy runs an end-to-end fuzzy test on the relay, injecting random requests and events
// at a high rate to test its robustness and reliability under load.
func TestFuzzy(t *testing.T) {
	config := defaultFuzzyConfig()
	start := time.Now()
	processed := atomic.Int64{}

	t.Logf("generating random request templates")
	templates := generateTemplates(10_000)
	t.Logf("finished in %v", time.Since(start))
	t.Logf("starting up the relay and swarm")

	ctx, cancel := context.WithTimeout(context.Background(), config.TestDuration)
	defer cancel()

	logger, err := newFileLogger("test.log")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	relay := rely.NewRelay(
		rely.WithLogger(logger.Logger),
	)

	// Step 1: register relay dummy function hooks.
	// The hooks simulate random failures and disconnects.
	relay.On.Connect = func(c rely.Client) {
		if rand.Float32() < config.DisconnectProbability {
			c.Disconnect()
			return
		}
	}
	relay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		processed.Add(1)
		if rand.Float32() < config.FailProbability {
			return rely.Fail("failed")
		}
		if rand.Float32() < config.DisconnectProbability {
			c.Disconnect()
		}
		return rely.Success()
	}
	relay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		processed.Add(1)
		if rand.Float32() < config.FailProbability {
			return nil, errors.New("failed")
		}
		if rand.Float32() < config.DisconnectProbability {
			c.Disconnect()
		}
		for _, sub := range c.Subscriptions() {
			if rand.Float32() < config.SubscriptionClosureProbability {
				sub.Close("idk bro")
			}
		}
		return nil, nil
	}
	relay.On.Count = func(c rely.Client, id string, filters nostr.Filters) (count int64, approx bool, err error) {
		processed.Add(1)
		if rand.Float32() < config.FailProbability {
			return 0, false, errors.New("failed")
		}
		if rand.Float32() < config.DisconnectProbability {
			c.Disconnect()
		}
		return rand.Int64N(1_000_000_000), true, nil
	}

	// Step 2: create the swarm with fuzzy client behaviors.
	swarm, err := swarm.New(config.Swarm, swarm.BehaviorDistribution{
		{P: 0.2, Behavior: fuzzyEventClient{templates}},
		{P: 0.5, Behavior: fuzzyReqClient{templates}},
		{P: 0.2, Behavior: fuzzyCountClient{templates}},
		{P: 0.1, Behavior: fuzzyCloseClient{templates}},
	})
	if err != nil {
		t.Fatalf("failed to create swarm: %v", err)
	}

	// Step 3: run everything.
	addr, cleanup := startRelayOnListener(t, relay)
	defer cleanup()

	go func() {
		ctx, cancel := context.WithTimeout(ctx, config.RelayDuration)
		defer cancel()
		go displayStats(ctx, "fuzzy", start, &processed, relay, swarm)
	}()

	go func() {
		ctx, cancel := context.WithTimeout(ctx, config.SwarmDuration)
		defer cancel()
		swarm.Run(ctx, addr)
	}()

	go func() { http.ListenAndServe(":6060", nil) }() // pprof

	select {
	case err := <-swarm.Err():
		t.Fatalf("swarm error: %v", err)

	case <-ctx.Done():
		// test passed, print stats last time
		clearScreen()
		printStats("fuzzy", start, &processed, relay, swarm)
	}
}

// fuzzyEventClient implements the [swarm.Behaviour] interface.
// It sends random EVENT requests generated from the templates and validates
// that the responses it receives have the expected labels ("OK", "NOTICE").
type fuzzyEventClient struct {
	t *templates
}

func (c fuzzyEventClient) NextRequest() []byte {
	return c.t.quickEvent()
}

func (c fuzzyEventClient) ValidateResponse(d *json.Decoder) error {
	return validateLabel("OK", "NOTICE")(d)
}

// fuzzyReqClient implements the [swarm.Behaviour] interface.
// It sends random REQ requests generated from the templates and validates
// that the responses it receives have the expected labels ("EOSE", "CLOSED", "EVENT", "NOTICE").
type fuzzyReqClient struct {
	t *templates
}

func (c fuzzyReqClient) NextRequest() []byte {
	return c.t.quickReq()
}

func (c fuzzyReqClient) ValidateResponse(d *json.Decoder) error {
	return validateLabel("EOSE", "CLOSED", "EVENT", "NOTICE")(d)
}

// fuzzyCountClient implements the [swarm.Behaviour] interface.
// It sends random COUNT requests generated from the templates and validates
// that the responses it receives have the expected labels ("CLOSED", "COUNT", "NOTICE").
type fuzzyCountClient struct {
	t *templates
}

func (c fuzzyCountClient) NextRequest() []byte {
	return c.t.quickCount()
}

func (c fuzzyCountClient) ValidateResponse(d *json.Decoder) error {
	return validateLabel("CLOSED", "COUNT", "NOTICE")(d)
}

// fuzzyCloseClient implements the [swarm.Behaviour] interface.
// It sends random CLOSE requests generated from the templates and validates
// that the responses it receives have the expected labels ("NOTICE").
type fuzzyCloseClient struct {
	t *templates
}

func (c fuzzyCloseClient) NextRequest() []byte {
	return c.t.quickClose()
}

func (c fuzzyCloseClient) ValidateResponse(d *json.Decoder) error {
	return validateLabel("NOTICE")(d)
}

// templates holds the request templates for REQ, COUNT, EVENT, and CLOSE requests.
// Its `quick` methods return a random request template with modified bytes, allowing
// much faster fuzzing compared to regenerating new requests from scratch.
type templates struct {
	req   [][]byte
	count [][]byte
	event [][]byte
	close [][]byte
}

// generateTemplates returns a new templates instance, pre-allocated with the given size for each request type.
func generateTemplates(size int) *templates {
	t := templates{
		req:   make([][]byte, size),
		count: make([][]byte, size),
		event: make([][]byte, size),
		close: make([][]byte, size),
	}

	for i := range size {
		t.req[i] = utils.RandomReqBytes()
		t.count[i] = utils.RandomCountBytes()
		t.event[i] = utils.RandomEventBytes()
		t.close[i] = utils.RandomCloseBytes()
	}
	return &t
}

// quickEvent returns a random EVENT request template.
// Only with a 5% chance, the template bytes are modified.
func (t *templates) quickEvent() []byte {
	i := rand.IntN(len(t.event))
	template := t.event[i]
	event := make([]byte, len(template))
	copy(event, template)

	if rand.Float32() < 0.05 {
		// this will inevitably invalidate the signature
		modifyBytes(event, 5)
	}
	return event
}

// quickReq returns a random REQ request template with modified bytes.
func (t *templates) quickReq() []byte {
	i := rand.IntN(len(t.req))
	template := t.req[i]
	req := make([]byte, len(template))
	copy(req, template)
	modifyBytes(req, 5)
	return req
}

// quickCount returns a random COUNT request template with modified bytes.
func (t *templates) quickCount() []byte {
	i := rand.IntN(len(t.count))
	template := t.count[i]
	count := make([]byte, len(template))
	copy(count, template)
	modifyBytes(count, 5)
	return count
}

// quickClose returns a random CLOSE request template with modified bytes.
func (t *templates) quickClose() []byte {
	i := rand.IntN(len(t.close))
	template := t.close[i]
	close := make([]byte, len(template))
	copy(close, template)
	modifyBytes(close, 5)
	return close
}

// modifyBytes modifies a buffer by randomly replacing bytes at random locations.
func modifyBytes(buf []byte, locations int) {
	l := len(buf)
	if l == 0 {
		return
	}

	for range locations {
		idx := rand.IntN(l)
		buf[idx] = byte(rand.IntN(256))
	}
}

// validateLabel returns a function that validates the label parsed from the JSON decoder
// against the expected list of labels.
func validateLabel(labels ...string) func(d *json.Decoder) error {
	return func(d *json.Decoder) error {
		label, err := parseLabel(d)
		if err != nil {
			return err
		}

		if !slices.Contains(labels, label) {
			return fmt.Errorf("label is not among the expected labels %v: %s", labels, label)
		}
		return nil
	}
}

// parseLabel parses a label from the JSON decoder.
func parseLabel(d *json.Decoder) (string, error) {
	token, err := d.Token()
	if err != nil {
		return "", fmt.Errorf("expected start of array '[', got: %w", err)
	}
	if token != json.Delim('[') {
		return "", fmt.Errorf("expected start of array '[', got: %v", token)
	}
	var label string
	if err := d.Decode(&label); err != nil {
		return "", fmt.Errorf("failed to read label: %w", err)
	}
	return label, nil
}

// displayStats displays the test statistics in real-time.
func displayStats(
	ctx context.Context,
	testName string,
	start time.Time,
	processed *atomic.Int64,
	relay *rely.Relay,
	swarm *swarm.T,
) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			clearScreen()
			printStats(testName, start, processed, relay, swarm)
		}
	}
}

// printStats prints the test statistics.
func printStats(
	testName string,
	start time.Time,
	processed *atomic.Int64,
	relay *rely.Relay,
	swarm *swarm.T,
) {
	fmt.Printf("---------------- test %s -----------------\n", testName)
	fmt.Printf("test time: %v\n", time.Since(start).Round(time.Second))
	fmt.Println("---------------------------------------")
	fmt.Printf("total connection attempts: %d\n", swarm.ConnectionAttempts())
	fmt.Printf("total connection established: %d\n", swarm.ConnectionEstablished())
	fmt.Printf("total data sent: %.3f GB\n", float64(swarm.DataSent())/(1024*1024*1024))
	fmt.Printf("total requests: %d\n", swarm.TotalRequests())
	fmt.Printf("total processed requests: %d\n", processed.Load())
	fmt.Println(relay.StatsReport())
}

// clearScreen clears the terminal screen.
func clearScreen() {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "cls")
	default:
		// Linux, macOS, etc..
		cmd = exec.Command("clear")
	}

	cmd.Stdout = os.Stdout
	cmd.Run()
}
