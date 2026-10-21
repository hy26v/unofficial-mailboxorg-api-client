// ABOUTME: Resumable, verified bulk mail moves with one authenticated session and an append-only checkpoint.
// ABOUTME: Every completed move is fingerprint-verified and durably recorded before the next mutation starts.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	maxMailMoves           = 10000
	defaultMoveOutputBytes = 1 << 20
	maxMoveOutputBytes     = 16 << 20
)

func init() {
	operations.Register(operations.Operation{
		Name:       "mail.move-many",
		Resource:   "mail",
		Action:     "move-many",
		Summary:    "Move a JSON plan of messages with verification and resumable checkpoints",
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"input":            {Type: "string", Required: true, Description: "@file or JSON array of {folder,id,to,expect_fingerprint?}"},
			"dry-run":          {Type: "bool", Required: false, Description: "validate and show normalized moves without authentication"},
			"preflight":        {Type: "bool", Required: false, Description: "authenticate and validate all sources and destinations without mutation"},
			"verify":           {Type: "bool", Required: false, Description: "required for live execution; verify every destination fingerprint"},
			"checkpoint":       {Type: "string", Required: false, Description: "required for live execution; owner-only append-only JSONL checkpoint"},
			"output":           {Type: "string", Required: false, Description: "write the full result to an owner-only JSON file"},
			"max-output-bytes": {Type: "int", Required: false, Description: "maximum full result size written to stdout (default 1048576)"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"key", "status", "source", "destination", "verified", "fingerprint"},
			ScalarFields: []string{"key", "status", "verified", "fingerprint"},
			DryRunFields: []string{"key", "status", "source", "destination", "verified", "fingerprint"},
		},
	})
}

type mailMoveManyInput struct {
	Folder            string `json:"folder"`
	ID                string `json:"id"`
	To                string `json:"to"`
	ExpectFingerprint string `json:"expect_fingerprint,omitempty"`
}

type mailMoveManyOptions struct {
	llm            bool
	dryRun         bool
	preflight      bool
	verify         bool
	input          string
	checkpoint     string
	output         string
	maxOutputBytes int
}

type preparedMailMoveMany struct {
	options mailMoveManyOptions
	moves   []mailMoveManyInput
}

func parseMailMoveManyOptions(args []string) (mailMoveManyOptions, *cliErrors.Error) {
	opts := mailMoveManyOptions{maxOutputBytes: defaultMoveOutputBytes}
	f, ferr := parseCommandFlags("mail.move-many", args,
		intFlag("max-output-bytes", minimumStructuredOutputBytes, maxMoveOutputBytes,
			fmt.Sprintf("--max-output-bytes must be an integer between %d and %d", minimumStructuredOutputBytes, maxMoveOutputBytes)))
	opts.llm = f.LLM()
	if ferr != nil {
		return opts, ferr
	}
	opts.dryRun = f.Bool("dry-run")
	opts.preflight = f.Bool("preflight")
	opts.verify = f.Bool("verify")
	opts.input = f.String("input")
	opts.checkpoint = f.String("checkpoint")
	opts.output = f.String("output")
	if maxOutputBytes, ok := f.Int("max-output-bytes"); ok {
		opts.maxOutputBytes = maxOutputBytes
	}
	if opts.input == "" {
		return opts, invalidArgument("--input is required, e.g. --input @moves.json")
	}
	if opts.dryRun && opts.preflight {
		return opts, invalidArgument("--dry-run and --preflight are mutually exclusive")
	}
	if !opts.dryRun && !opts.preflight {
		if !opts.verify {
			return opts, invalidArgument("live mail move-many requires --verify")
		}
		if strings.TrimSpace(opts.checkpoint) == "" {
			return opts, invalidArgument("live mail move-many requires --checkpoint <file>")
		}
	}
	if opts.output != "" && strings.TrimSpace(opts.output) == "" {
		return opts, invalidArgument("--output must not be blank")
	}
	if opts.checkpoint != "" && opts.output != "" {
		if pathsReferToSameFile(opts.checkpoint, opts.output) {
			return opts, invalidArgument("--output and --checkpoint must be different files")
		}
	}
	if strings.HasPrefix(opts.input, "@") && opts.output != "" {
		inputPath := strings.TrimPrefix(opts.input, "@")
		if pathsReferToSameFile(inputPath, opts.output) {
			return opts, invalidArgument("--output must not overwrite the file-backed --input plan")
		}
	}
	return opts, nil
}

func parseMailMoveManyInput(raw string) ([]mailMoveManyInput, error) {
	var contents []byte
	if strings.HasPrefix(raw, "@") {
		path := strings.TrimPrefix(raw, "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("could not read --input file %s: %w", path, err)
		}
		contents = data
	} else {
		contents = []byte(raw)
	}
	var moves []mailMoveManyInput
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&moves); err != nil {
		return nil, fmt.Errorf("--input is not a valid JSON array of moves: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("--input contains multiple JSON values")
		}
		return nil, fmt.Errorf("--input is not a valid JSON array of moves: %w", err)
	}
	if moves == nil {
		return nil, fmt.Errorf("--input must be a JSON array, not null")
	}
	if len(moves) == 0 {
		return nil, fmt.Errorf("--input contains no moves")
	}
	if len(moves) > maxMailMoves {
		return nil, fmt.Errorf("--input contains %d moves; maximum is %d", len(moves), maxMailMoves)
	}
	seenSources := make(map[string]bool, len(moves))
	for i := range moves {
		plan, err := newMailMovePlan(moves[i].ID, moves[i].Folder, moves[i].To)
		if err != nil {
			return nil, fmt.Errorf("move %d: %w", i, err)
		}
		moves[i].ID, moves[i].Folder, moves[i].To = plan.SourceID, plan.SourceFolder, plan.DestinationFolder
		if moves[i].ExpectFingerprint != "" && !validMailFingerprint(moves[i].ExpectFingerprint) {
			return nil, fmt.Errorf("move %d expect_fingerprint is not a valid sha256 fingerprint", i)
		}
		sourceKey := plan.SourceFolder + "\x00" + plan.SourceID
		if seenSources[sourceKey] {
			return nil, fmt.Errorf("move %d repeats source %s/%s", i, plan.SourceFolder, plan.SourceID)
		}
		seenSources[sourceKey] = true
	}
	return moves, nil
}

func prepareMailMoveMany(args []string) (preparedMailMoveMany, *cliErrors.Error) {
	opts, optionErr := parseMailMoveManyOptions(args)
	prepared := preparedMailMoveMany{options: opts}
	if optionErr != nil {
		return prepared, optionErr
	}
	moves, inputErr := parseMailMoveManyInput(opts.input)
	if inputErr != nil {
		return prepared, &cliErrors.Error{Code: "invalid_argument", Message: inputErr.Error()}
	}
	prepared.moves = moves
	return prepared, nil
}

func mailMoveKey(move mailMoveManyInput) string {
	encoded, _ := json.Marshal(struct {
		Folder            string `json:"folder"`
		ID                string `json:"id"`
		To                string `json:"to"`
		ExpectFingerprint string `json:"expect_fingerprint,omitempty"`
	}{move.Folder, move.ID, move.To, move.ExpectFingerprint})
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", sum)
}

// mailMoveCheckpoint is one durable checkpoint line. Verified is false when the provider
// accepted the move but the destination did not verify; Fingerprint then holds the source
// fingerprint, and a rerun verifies the destination against it instead of moving again.
type mailMoveCheckpoint struct {
	Key                 string `json:"key"`
	SourceFolder        string `json:"source_folder"`
	SourceID            string `json:"source_id"`
	DestinationFolder   string `json:"destination_folder"`
	DestinationID       string `json:"destination_id"`
	ExpectedFingerprint string `json:"expected_fingerprint,omitempty"`
	Fingerprint         string `json:"fingerprint"`
	Verified            bool   `json:"verified"`
}

func loadMailMoveCheckpoints(path string) (map[string]mailMoveCheckpoint, error) {
	records := map[string]mailMoveCheckpoint{}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("checkpoint path is not a regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			return nil, fmt.Errorf("checkpoint line %d is blank", line)
		}
		var record mailMoveCheckpoint
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("checkpoint line %d is invalid: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("checkpoint line %d contains multiple JSON values", line)
			}
			return nil, fmt.Errorf("checkpoint line %d has invalid trailing JSON: %w", line, err)
		}
		move := mailMoveManyInput{Folder: record.SourceFolder, ID: record.SourceID, To: record.DestinationFolder, ExpectFingerprint: record.ExpectedFingerprint}
		if !validMailFingerprint(record.Fingerprint) || strings.TrimSpace(record.DestinationID) == "" || record.Key != mailMoveKey(move) {
			return nil, fmt.Errorf("checkpoint line %d is incomplete or has an invalid key", line)
		}
		if previous, exists := records[record.Key]; exists && previous != record {
			// A later verified record may confirm an earlier unverified one for the same
			// destination; any other difference is a conflict.
			confirmed := previous
			confirmed.Verified = true
			if previous.Verified || !record.Verified || confirmed != record {
				return nil, fmt.Errorf("checkpoint line %d conflicts with an earlier record for %s", line, record.Key)
			}
		}
		records[record.Key] = record
		if len(records) > maxMailMoves {
			return nil, fmt.Errorf("checkpoint contains more than %d distinct moves", maxMailMoves)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func appendMailMoveCheckpoint(file *os.File, record mailMoveCheckpoint) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	written, err := file.Write(encoded)
	if err != nil {
		return err
	}
	if written != len(encoded) {
		return io.ErrShortWrite
	}
	return file.Sync()
}

type mailMoveManyResultItem struct {
	Key         string           `json:"key"`
	Status      string           `json:"status"`
	Source      mailMoveLocation `json:"source"`
	Destination mailMoveLocation `json:"destination"`
	Verified    bool             `json:"verified"`
	Fingerprint string           `json:"fingerprint,omitempty"`
}

type mailMoveManyDocument struct {
	Items      []mailMoveManyResultItem `json:"items"`
	Count      int                      `json:"count"`
	Moved      int                      `json:"moved"`
	Resumed    int                      `json:"resumed"`
	Checkpoint string                   `json:"checkpoint,omitempty"`
	DryRun     bool                     `json:"dry_run,omitempty"`
	Preflight  bool                     `json:"preflight,omitempty"`
	// Failed and Remaining are set only when a live run stops part way.
	Failed    *mailMoveManyFailure `json:"failed,omitempty"`
	Remaining *int                 `json:"remaining,omitempty"`
}

// mailMoveManyFailure describes the move that stopped a live run. Moved reports whether
// the provider accepted the move (the message may now be in the destination), and
// Checkpointed whether the checkpoint has a record for it, so a rerun resumes from there.
type mailMoveManyFailure struct {
	Index        int              `json:"index"`
	Key          string           `json:"key"`
	Source       mailMoveLocation `json:"source"`
	Destination  mailMoveLocation `json:"destination"`
	Moved        bool             `json:"moved"`
	Verified     bool             `json:"verified"`
	Checkpointed bool             `json:"checkpointed"`
	Code         string           `json:"code"`
	Message      string           `json:"message"`
}

func runMailMoveMany(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	prepared, preparationErr := prepareMailMoveMany(args)
	if preparationErr != nil {
		return exitWithError(stderr, prepared.options.llm, preparationErr)
	}
	return runPreparedMailMoveMany(stdout, stderr, prepared, httpClient, baseURL, sessionPath, getenv)
}

func runPreparedMailMoveMany(stdout, stderr io.Writer, prepared preparedMailMoveMany, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	opts, moves := prepared.options, prepared.moves
	if opts.output == "" {
		estimatedBytes := 256
		for _, move := range moves {
			estimatedBytes += 1024 + len(move.Folder) + len(move.ID) + len(move.To)
		}
		if estimatedBytes > opts.maxOutputBytes {
			cerr := &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("mail move-many may produce about %d bytes, exceeding --max-output-bytes=%d; use --output <file> before executing", estimatedBytes, opts.maxOutputBytes)}
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
	}

	document := mailMoveManyDocument{Items: make([]mailMoveManyResultItem, 0, len(moves)), Count: len(moves), DryRun: opts.dryRun, Preflight: opts.preflight}
	if opts.dryRun {
		for _, move := range moves {
			plan, _ := newMailMovePlan(move.ID, move.Folder, move.To)
			output := newMailMoveOutput(plan)
			document.Items = append(document.Items, mailMoveManyResultItem{Key: mailMoveKey(move), Status: "planned", Source: output.Source, Destination: output.Destination, Fingerprint: move.ExpectFingerprint})
		}
		return emitMailMoveManyDocument(stdout, stderr, opts, document)
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, opts.llm, authErr)
	}
	destinations := map[string]bool{}
	for _, move := range moves {
		destinations[move.To] = true
	}
	destinationIDs := make([]string, 0, len(destinations))
	for destination := range destinations {
		destinationIDs = append(destinationIDs, destination)
	}
	sort.Strings(destinationIDs)
	for _, destination := range destinationIDs {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		detail, err := getFolder(ctx, c, sess, destination)
		cancel()
		if err != nil {
			cerr := toCLIError(err, "api_error")
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		if detail.Module != "mail" {
			cerr := &cliErrors.Error{Code: "invalid_argument", Message: "destination folder is not a mail folder: " + destination}
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
	}

	if opts.preflight {
		for _, move := range moves {
			plan, _ := newMailMovePlan(move.ID, move.Folder, move.To)
			ctx, cancel := context.WithTimeout(context.Background(), 2*requestTimeout)
			ready, cerr := inspectMailMoveSource(ctx, c, sess, plan, move.ExpectFingerprint)
			cancel()
			if cerr != nil {
				writeErr(stderr, opts.llm, cerr)
				return cliErrors.ExitCodeFor(cerr)
			}
			document.Items = append(document.Items, mailMoveManyResultItem{Key: mailMoveKey(move), Status: "ready", Source: ready.Source, Destination: ready.Destination, Fingerprint: ready.Fingerprint})
		}
		return emitMailMoveManyDocument(stdout, stderr, opts, document)
	}

	checkpoints, err := loadMailMoveCheckpoints(opts.checkpoint)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not load --checkpoint: " + err.Error()}
		writeErr(stderr, opts.llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	checkpointFile, err := openPrivateAppendFile(opts.checkpoint)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not open --checkpoint: " + err.Error()}
		writeErr(stderr, opts.llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	defer checkpointFile.Close()
	document.Checkpoint = opts.checkpoint

	// stop ends a live run part way: it emits the partial result document to stdout or
	// --output, then writes the error with the counts, and keeps the error's exit code.
	stop := func(index int, failure mailMoveManyFailure, cerr *cliErrors.Error) int {
		failure.Index, failure.Code, failure.Message = index, cerr.Code, cerr.Message
		remaining := len(moves) - index - 1
		document.Failed, document.Remaining = &failure, &remaining
		emitMailMoveManyDocument(stdout, stderr, opts, document)
		destination := "stdout"
		if opts.output != "" {
			destination = "--output " + opts.output
		}
		summary := fmt.Sprintf("mail move-many stopped at move %d of %d (moved %d, resumed %d, %d not attempted; partial result written to %s): %s",
			index+1, len(moves), document.Moved, document.Resumed, remaining, destination, cerr.Message)
		writeErr(stderr, opts.llm, &cliErrors.Error{Code: cerr.Code, Message: summary})
		return cliErrors.ExitCodeFor(cerr)
	}

	// olderCheckpointHint explains a checkpoint fingerprint that no longer matches. mbx now
	// reads mail dates in UTC, so a checkpoint written by an older mbx version does not
	// match on an account whose time zone is not UTC. The fail-safe stop stays.
	const olderCheckpointHint = "a checkpoint written by an older mbx version (before mail dates were read in UTC) does not match; " +
		"check the destination message by hand, remove the completed moves from the plan, and use a new checkpoint file"

	for index, move := range moves {
		key := mailMoveKey(move)
		plan, _ := newMailMovePlan(move.ID, move.Folder, move.To)
		source := mailMoveLocation{Folder: move.Folder, ID: stringPointer(move.ID)}
		if record, exists := checkpoints[key]; exists {
			destination := mailMoveLocation{Folder: record.DestinationFolder, ID: stringPointer(record.DestinationID)}
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
			metadata, verifyErr := fetchMailMetadata(ctx, c, sess, record.DestinationID, record.DestinationFolder)
			cancel()
			if verifyErr != nil || metadata.Fingerprint != record.Fingerprint {
				code, message := "checkpoint_invalid", "checkpoint destination no longer verifies for "+key
				if !record.Verified {
					code, message = "verification_failed", "an earlier run moved "+key+" but its destination still does not verify; check the destination message by hand"
				}
				if verifyErr != nil {
					message += ": " + verifyErr.Error()
				} else {
					message += "; " + olderCheckpointHint
				}
				failure := mailMoveManyFailure{Key: key, Source: source, Destination: destination, Moved: true, Verified: record.Verified, Checkpointed: true}
				return stop(index, failure, &cliErrors.Error{Code: code, Message: message})
			}
			if !record.Verified {
				record.Verified = true
				if err := appendMailMoveCheckpoint(checkpointFile, record); err != nil {
					failure := mailMoveManyFailure{Key: key, Source: source, Destination: destination, Moved: true, Verified: true, Checkpointed: true}
					return stop(index, failure, &cliErrors.Error{Code: "checkpoint_write_failed", Message: "destination now verifies, but checkpoint write failed; refusing further mutations: " + err.Error()})
				}
				checkpoints[key] = record
			}
			document.Items = append(document.Items, mailMoveManyResultItem{
				Key: key, Status: "resumed", Source: source, Destination: destination, Verified: true, Fingerprint: record.Fingerprint,
			})
			document.Resumed++
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 4*requestTimeout)
		moved, moveErr := executeMailMoveWithResolvedDestination(ctx, c, sess, plan, true, move.ExpectFingerprint, true)
		cancel()
		if moveErr != nil {
			failure := mailMoveManyFailure{Key: key, Source: source, Destination: mailMoveLocation{Folder: move.To}}
			if moveErr.Code == "verification_failed" && moved.Destination.ID != nil && validMailFingerprint(moved.Fingerprint) {
				// The provider accepted the move, so the source is gone. Record the move
				// unverified, so a rerun checks the destination instead of failing to find
				// the source.
				failure.Destination, failure.Moved = moved.Destination, true
				record := mailMoveCheckpoint{
					Key: key, SourceFolder: move.Folder, SourceID: move.ID,
					DestinationFolder: move.To, DestinationID: *moved.Destination.ID,
					ExpectedFingerprint: move.ExpectFingerprint, Fingerprint: moved.Fingerprint, Verified: false,
				}
				if err := appendMailMoveCheckpoint(checkpointFile, record); err == nil {
					failure.Checkpointed = true
				} else {
					moveErr = &cliErrors.Error{Code: moveErr.Code, Message: moveErr.Message + "; the checkpoint write also failed: " + err.Error()}
				}
			}
			return stop(index, failure, moveErr)
		}
		record := mailMoveCheckpoint{
			Key: key, SourceFolder: move.Folder, SourceID: move.ID,
			DestinationFolder: move.To, DestinationID: *moved.Destination.ID,
			ExpectedFingerprint: move.ExpectFingerprint, Fingerprint: moved.Fingerprint, Verified: moved.Verified,
		}
		if err := appendMailMoveCheckpoint(checkpointFile, record); err != nil {
			failure := mailMoveManyFailure{Key: key, Source: moved.Source, Destination: moved.Destination, Moved: true, Verified: true}
			return stop(index, failure, &cliErrors.Error{Code: "checkpoint_write_failed", Message: "move completed and verified, but checkpoint write failed; refusing further mutations: " + err.Error()})
		}
		checkpoints[key] = record
		document.Items = append(document.Items, mailMoveManyResultItem{Key: key, Status: "moved", Source: moved.Source, Destination: moved.Destination, Verified: true, Fingerprint: moved.Fingerprint})
		document.Moved++
	}
	return emitMailMoveManyDocument(stdout, stderr, opts, document)
}

func emitMailMoveManyDocument(stdout, stderr io.Writer, opts mailMoveManyOptions, document mailMoveManyDocument) int {
	payload, err := json.Marshal(document)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: "could not encode mail move-many output"}
		writeErr(stderr, opts.llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	payload = append(payload, '\n')
	if opts.output != "" {
		if err := writePrivateFile(opts.output, payload); err != nil {
			cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not write --output file: " + err.Error()}
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		if opts.llm {
			render.CompactJSON(stdout, struct {
				SavedTo      string               `json:"saved_to"`
				BytesWritten int                  `json:"bytes_written"`
				Count        int                  `json:"count"`
				Moved        int                  `json:"moved"`
				Resumed      int                  `json:"resumed"`
				Failed       *mailMoveManyFailure `json:"failed,omitempty"`
				Remaining    *int                 `json:"remaining,omitempty"`
			}{SavedTo: opts.output, BytesWritten: len(payload), Count: document.Count, Moved: document.Moved, Resumed: document.Resumed, Failed: document.Failed, Remaining: document.Remaining})
		} else {
			render.KeyValueTable(stdout, append([]render.Field{{Name: "saved_to", Value: opts.output}, {Name: "bytes_written", Value: strconv.Itoa(len(payload))}, {Name: "count", Value: strconv.Itoa(document.Count)}, {Name: "moved", Value: strconv.Itoa(document.Moved)}, {Name: "resumed", Value: strconv.Itoa(document.Resumed)}}, mailMoveManyFailureFields(document)...))
		}
		return 0
	}
	if len(payload) > opts.maxOutputBytes {
		cerr := &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("mail move-many output is %d bytes, exceeding --max-output-bytes=%d; use --output <file>", len(payload), opts.maxOutputBytes)}
		writeErr(stderr, opts.llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	if opts.llm {
		_, _ = stdout.Write(payload)
		return 0
	}
	rows := make([][]string, len(document.Items))
	for i, item := range document.Items {
		destinationID := ""
		if item.Destination.ID != nil {
			destinationID = *item.Destination.ID
		}
		rows[i] = []string{item.Status, item.Source.Folder, *item.Source.ID, item.Destination.Folder, destinationID, strconv.FormatBool(item.Verified)}
	}
	render.Table(stdout, []string{"status", "source_folder", "source_id", "destination_folder", "destination_id", "verified"}, rows)
	if fields := mailMoveManyFailureFields(document); len(fields) > 0 {
		fmt.Fprintln(stdout)
		render.KeyValueTable(stdout, fields)
	}
	return 0
}

// mailMoveManyFailureFields renders the failed move and the count of moves not attempted
// for human output; it is empty when the run did not stop part way.
func mailMoveManyFailureFields(document mailMoveManyDocument) []render.Field {
	if document.Failed == nil {
		return nil
	}
	failure := document.Failed
	destinationID := ""
	if failure.Destination.ID != nil {
		destinationID = *failure.Destination.ID
	}
	remaining := 0
	if document.Remaining != nil {
		remaining = *document.Remaining
	}
	return []render.Field{
		{Name: "failed_index", Value: strconv.Itoa(failure.Index)},
		{Name: "failed_source", Value: failure.Source.Folder + "/" + *failure.Source.ID},
		{Name: "failed_destination", Value: failure.Destination.Folder + "/" + destinationID},
		{Name: "failed_moved", Value: strconv.FormatBool(failure.Moved)},
		{Name: "failed_checkpointed", Value: strconv.FormatBool(failure.Checkpointed)},
		{Name: "failed_code", Value: failure.Code},
		{Name: "remaining", Value: strconv.Itoa(remaining)},
	}
}
