package conversationmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
)

var (
	ErrInvalidSourceRead     = errors.New("conversation memory source read is invalid")
	ErrSourceNotAuthorized   = errors.New("conversation memory source is not authorized")
	ErrSourceCursorInvalid   = errors.New("conversation memory source cursor is invalid")
	ErrSourceMessagesInvalid = errors.New("conversation memory source messages are invalid")
)

type ActiveSnapshotReader interface {
	Active(context.Context, uuid.UUID) (*Snapshot, error)
}

type SourceMessageReader interface {
	ReadSourceMessages(context.Context, uuid.UUID, uuid.UUID, []int64) ([]conversation.Message, error)
}

type SourceTokenCounter interface {
	Count(context.Context, string) (int, error)
}

type SourceRecoveryConfig struct {
	ActiveSnapshots ActiveSnapshotReader
	Messages        SourceMessageReader
	TokenCounter    SourceTokenCounter
	MaxMessages     int
	MaxTokens       int
}

type SourceRecovery struct {
	activeSnapshots ActiveSnapshotReader
	messages        SourceMessageReader
	tokenCounter    SourceTokenCounter
	maxMessages     int
	maxTokens       int
}

func NewSourceRecovery(config SourceRecoveryConfig) (*SourceRecovery, error) {
	if config.ActiveSnapshots == nil || config.Messages == nil || config.TokenCounter == nil ||
		config.MaxMessages < 1 || config.MaxMessages > 20 || config.MaxTokens < 256 || config.MaxTokens > 8192 {
		return nil, ErrInvalidSourceRead
	}
	return &SourceRecovery{
		activeSnapshots: config.ActiveSnapshots, messages: config.Messages, tokenCounter: config.TokenCounter,
		maxMessages: config.MaxMessages, maxTokens: config.MaxTokens,
	}, nil
}

type SourceReadRequest struct {
	Actor              conversation.Actor
	ConversationID     uuid.UUID
	EntryID            string
	SourceMessageSeqs  []int64
	ContinuationCursor string
	Query              string
	ContentOffsetRunes *int
}

type SourceReadMode string

const (
	SourceReadModeSequential SourceReadMode = "sequential"
	SourceReadModeRelevant   SourceReadMode = "relevant"
)

type SourceMessage struct {
	MessageRef         string                   `json:"messageRef"`
	Seq                int64                    `json:"seq"`
	Role               conversation.MessageRole `json:"role"`
	Content            string                   `json:"content"`
	ContentOffsetRunes int                      `json:"contentOffsetRunes"`
	ContentEndRunes    int                      `json:"contentEndRunes"`
	MessageTotalRunes  int                      `json:"messageTotalRunes"`
	ContentComplete    bool                     `json:"contentComplete"`
	WindowComplete     bool                     `json:"windowComplete"`
	MatchScore         int                      `json:"matchScore,omitempty"`
}

type SourceReadResult struct {
	Mode                  SourceReadMode  `json:"mode"`
	Messages              []SourceMessage `json:"messages"`
	HasMore               bool            `json:"hasMore"`
	ContinuationAvailable bool            `json:"continuationAvailable"`
	ContinuationCursor    string          `json:"continuationCursor,omitempty"`
	TruncatedByTurnBudget bool            `json:"truncatedByTurnBudget,omitempty"`
}

type sourceRecoveryRunContextKey struct{}

type sourceRecoveryCursorState struct {
	UserID             uuid.UUID
	ConversationID     uuid.UUID
	SnapshotID         uuid.UUID
	Sequences          []int64
	Mode               SourceReadMode
	Query              string
	MessageIndex       int
	ContentOffsetRunes int
	Windows            []sourceRecoveryWindow
	WindowIndex        int
	WindowOffsetRunes  int
}

type sourceRecoveryWindow struct {
	Sequence   int64
	StartRunes int
	EndRunes   int
	Score      int
}

type sourceRecoveryRunState struct {
	operationMu sync.Mutex
	mu          sync.Mutex
	cursors     map[string]sourceRecoveryCursorState
}

func WithSourceRecoveryRun(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	if _, ok := sourceRecoveryRunStateFromContext(ctx); ok {
		return ctx
	}
	return context.WithValue(ctx, sourceRecoveryRunContextKey{}, &sourceRecoveryRunState{
		cursors: make(map[string]sourceRecoveryCursorState),
	})
}

func sourceRecoveryRunStateFromContext(ctx context.Context) (*sourceRecoveryRunState, bool) {
	if ctx == nil {
		return nil, false
	}
	state, ok := ctx.Value(sourceRecoveryRunContextKey{}).(*sourceRecoveryRunState)
	return state, ok && state != nil
}

func (r *SourceRecovery) Read(ctx context.Context, request SourceReadRequest) (SourceReadResult, error) {
	if r == nil || r.activeSnapshots == nil || r.messages == nil || r.tokenCounter == nil ||
		request.Actor.UserID == uuid.Nil || request.ConversationID == uuid.Nil {
		return SourceReadResult{}, ErrInvalidSourceRead
	}
	runState, ok := sourceRecoveryRunStateFromContext(ctx)
	if !ok {
		return SourceReadResult{}, ErrSourceCursorInvalid
	}
	runState.operationMu.Lock()
	defer runState.operationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return SourceReadResult{}, err
	}
	request.EntryID = strings.TrimSpace(request.EntryID)
	request.ContinuationCursor = strings.TrimSpace(request.ContinuationCursor)
	request.Query = strings.TrimSpace(request.Query)
	selectors := 0
	if request.EntryID != "" {
		selectors++
	}
	if len(request.SourceMessageSeqs) > 0 {
		selectors++
	}
	if request.ContinuationCursor != "" {
		selectors++
	}
	if selectors != 1 || len(request.ContinuationCursor) > 128 ||
		(request.ContentOffsetRunes != nil && *request.ContentOffsetRunes < 0) ||
		len([]rune(request.Query)) > 256 ||
		(request.ContinuationCursor != "" && (request.Query != "" || request.ContentOffsetRunes != nil)) ||
		(request.Query != "" && request.ContentOffsetRunes != nil) ||
		(request.ContentOffsetRunes != nil &&
			(request.EntryID != "" || len(request.SourceMessageSeqs) != 1)) {
		return SourceReadResult{}, ErrInvalidSourceRead
	}

	var cursorState sourceRecoveryCursorState
	if request.ContinuationCursor != "" {
		var found bool
		cursorState, found = runState.load(request.ContinuationCursor)
		if !found || cursorState.UserID != request.Actor.UserID ||
			cursorState.ConversationID != request.ConversationID {
			return SourceReadResult{}, ErrSourceCursorInvalid
		}
	}
	active, err := r.activeSnapshots.Active(ctx, request.ConversationID)
	if err != nil {
		return SourceReadResult{}, fmt.Errorf("load active conversation memory snapshot: %w", err)
	}
	if active == nil || active.ConversationID != request.ConversationID || active.Status != SnapshotStatusActive ||
		active.Validate() != nil {
		return SourceReadResult{}, ErrInvalidSnapshot
	}
	if request.ContinuationCursor != "" {
		if cursorState.SnapshotID != active.ID || !validSourceCursorState(cursorState) {
			return SourceReadResult{}, ErrSourceCursorInvalid
		}
	} else {
		sequences, authorizeErr := authorizedSourceSequences(active.Payload, request)
		if authorizeErr != nil {
			return SourceReadResult{}, authorizeErr
		}
		cursorState = sourceRecoveryCursorState{
			UserID: request.Actor.UserID, ConversationID: request.ConversationID,
			SnapshotID: active.ID, Sequences: sequences, Mode: SourceReadModeSequential,
		}
		if request.ContentOffsetRunes != nil {
			cursorState.ContentOffsetRunes = *request.ContentOffsetRunes
		}
		if request.Query != "" {
			cursorState.Mode = SourceReadModeRelevant
			cursorState.Query = request.Query
		}
	}
	readSequences := cursorState.Sequences
	if cursorState.Mode == SourceReadModeSequential {
		readSequences = cursorState.Sequences[cursorState.MessageIndex:]
	}
	messages, err := r.messages.ReadSourceMessages(
		ctx, request.Actor.UserID, request.ConversationID, append([]int64(nil), readSequences...),
	)
	if err != nil {
		return SourceReadResult{}, fmt.Errorf("read authorized conversation memory source messages: %w", err)
	}
	orderedMessages, err := orderedSourceMessages(request.ConversationID, readSequences, messages)
	if err != nil {
		return SourceReadResult{}, err
	}
	if cursorState.Mode == SourceReadModeRelevant && len(cursorState.Windows) == 0 {
		cursorState.Windows = buildRelevantSourceWindows(orderedMessages, cursorState.Query)
		if len(cursorState.Windows) == 0 {
			return SourceReadResult{Mode: SourceReadModeRelevant, Messages: []SourceMessage{}}, nil
		}
		cursorState.WindowOffsetRunes = cursorState.Windows[0].StartRunes
	}
	var result SourceReadResult
	var nextState *sourceRecoveryCursorState
	var nextCursor string
	if cursorState.Mode == SourceReadModeRelevant {
		result, nextState, nextCursor, err = r.buildRelevantPage(ctx, cursorState, orderedMessages)
	} else {
		result, nextState, nextCursor, err = r.buildPage(ctx, cursorState, orderedMessages)
	}
	if err != nil {
		return SourceReadResult{}, err
	}
	runState.commit(request.ContinuationCursor, nextCursor, nextState)
	return result, nil
}

func validSourceCursorState(state sourceRecoveryCursorState) bool {
	if len(state.Sequences) == 0 {
		return false
	}
	switch state.Mode {
	case SourceReadModeSequential:
		return state.MessageIndex >= 0 && state.MessageIndex < len(state.Sequences) &&
			state.ContentOffsetRunes >= 0
	case SourceReadModeRelevant:
		if strings.TrimSpace(state.Query) == "" || state.WindowIndex < 0 ||
			state.WindowIndex >= len(state.Windows) || state.WindowOffsetRunes < 0 {
			return false
		}
		current := state.Windows[state.WindowIndex]
		return current.Sequence > 0 && current.StartRunes >= 0 && current.EndRunes > current.StartRunes &&
			state.WindowOffsetRunes >= current.StartRunes && state.WindowOffsetRunes < current.EndRunes
	default:
		return false
	}
}

func (s *sourceRecoveryRunState) load(cursor string) (sourceRecoveryCursorState, bool) {
	if s == nil {
		return sourceRecoveryCursorState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.cursors[cursor]
	state.Sequences = append([]int64(nil), state.Sequences...)
	state.Windows = append([]sourceRecoveryWindow(nil), state.Windows...)
	return state, ok
}

func (s *sourceRecoveryRunState) commit(
	consumedCursor, nextCursor string,
	nextState *sourceRecoveryCursorState,
) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if consumedCursor != "" {
		delete(s.cursors, consumedCursor)
	}
	if nextCursor != "" && nextState != nil {
		copy := *nextState
		copy.Sequences = append([]int64(nil), nextState.Sequences...)
		copy.Windows = append([]sourceRecoveryWindow(nil), nextState.Windows...)
		s.cursors[nextCursor] = copy
	}
}

func authorizedSourceSequences(payload Payload, request SourceReadRequest) ([]int64, error) {
	if request.EntryID != "" {
		entry, found := sourceEntryByID(payload, request.EntryID)
		if !found || entry.Status == EntryStatusSuperseded {
			return nil, ErrSourceNotAuthorized
		}
		return append([]int64(nil), entry.SourceMessageSeqs...), nil
	}
	if len(request.SourceMessageSeqs) == 0 || len(request.SourceMessageSeqs) > 32 ||
		!slices.IsSorted(request.SourceMessageSeqs) {
		return nil, ErrSourceNotAuthorized
	}
	allowed := make(map[int64]struct{})
	for _, entry := range sourceEntries(payload) {
		if entry.Status == EntryStatusSuperseded {
			continue
		}
		for _, sequence := range entry.SourceMessageSeqs {
			allowed[sequence] = struct{}{}
		}
	}
	for index, sequence := range request.SourceMessageSeqs {
		if sequence < 1 || index > 0 && request.SourceMessageSeqs[index-1] == sequence {
			return nil, ErrSourceNotAuthorized
		}
		if _, ok := allowed[sequence]; !ok {
			return nil, ErrSourceNotAuthorized
		}
	}
	return append([]int64(nil), request.SourceMessageSeqs...), nil
}

func sourceEntryByID(payload Payload, entryID string) (Entry, bool) {
	for _, entry := range sourceEntries(payload) {
		if entry.EntryID == entryID {
			return entry, true
		}
	}
	return Entry{}, false
}

func sourceEntries(payload Payload) []Entry {
	indexed := collectEntries(&payload)
	entries := make([]Entry, 0, len(indexed))
	for _, current := range indexed {
		if current.entry != nil {
			entries = append(entries, *current.entry)
		}
	}
	return entries
}

func orderedSourceMessages(
	conversationID uuid.UUID,
	sequences []int64,
	messages []conversation.Message,
) ([]conversation.Message, error) {
	if len(sequences) == 0 || len(messages) != len(sequences) || !slices.IsSorted(sequences) {
		return nil, ErrSourceMessagesInvalid
	}
	bySequence := make(map[int64]conversation.Message, len(messages))
	for _, message := range messages {
		if message.ID == uuid.Nil || message.ConversationID != conversationID || message.Seq < 1 ||
			!message.Role.Valid() || strings.TrimSpace(message.Content) == "" {
			return nil, ErrSourceMessagesInvalid
		}
		if _, duplicate := bySequence[message.Seq]; duplicate {
			return nil, ErrSourceMessagesInvalid
		}
		bySequence[message.Seq] = message
	}
	result := make([]conversation.Message, 0, len(sequences))
	for _, sequence := range sequences {
		message, ok := bySequence[sequence]
		if !ok {
			return nil, ErrSourceMessagesInvalid
		}
		result = append(result, message)
	}
	return result, nil
}

func (r *SourceRecovery) buildPage(
	ctx context.Context,
	state sourceRecoveryCursorState,
	messages []conversation.Message,
) (SourceReadResult, *sourceRecoveryCursorState, string, error) {
	if len(messages) != len(state.Sequences)-state.MessageIndex || state.ContentOffsetRunes < 0 {
		return SourceReadResult{}, nil, "", ErrSourceMessagesInvalid
	}
	result := SourceReadResult{
		Mode: SourceReadModeSequential, Messages: make([]SourceMessage, 0, min(r.maxMessages, len(messages))),
	}
	next := state
	nextCursor := uuid.NewString()
	startIndex := state.MessageIndex
	for len(result.Messages) < r.maxMessages && next.MessageIndex < len(next.Sequences) {
		message := messages[next.MessageIndex-startIndex]
		contentRunes := []rune(message.Content)
		if next.ContentOffsetRunes >= len(contentRunes) {
			return SourceReadResult{}, nil, "", ErrSourceCursorInvalid
		}
		fullMessage := sourceMessageProjection(message, contentRunes[next.ContentOffsetRunes:], next.ContentOffsetRunes, true)
		afterFull := next
		afterFull.MessageIndex++
		afterFull.ContentOffsetRunes = 0
		candidate := appendSourceMessage(result, fullMessage, afterFull.MessageIndex < len(afterFull.Sequences), nextCursor)
		fits, err := r.resultFits(ctx, candidate)
		if err != nil {
			return SourceReadResult{}, nil, "", err
		}
		if fits {
			result = candidate
			next = afterFull
			continue
		}

		prefixLength, err := r.largestFittingPrefix(
			ctx, result, message, contentRunes, next.ContentOffsetRunes, nextCursor,
		)
		if err != nil {
			return SourceReadResult{}, nil, "", err
		}
		if prefixLength == 0 {
			if len(result.Messages) == 0 {
				return SourceReadResult{}, nil, "", ErrInvalidSourceRead
			}
			result.HasMore = true
			result.ContinuationCursor = nextCursor
			result.ContinuationAvailable = true
			break
		}
		remaining := contentRunes[next.ContentOffsetRunes:]
		contentComplete := prefixLength == len(remaining)
		result.Messages = append(result.Messages, sourceMessageProjection(
			message, remaining[:prefixLength], next.ContentOffsetRunes, contentComplete,
		))
		if contentComplete {
			next.MessageIndex++
			next.ContentOffsetRunes = 0
		} else {
			next.ContentOffsetRunes += prefixLength
		}
		result.HasMore = next.MessageIndex < len(next.Sequences)
		result.ContinuationCursor = nextCursor
		result.ContinuationAvailable = result.HasMore
		break
	}
	if next.MessageIndex < len(next.Sequences) && !result.HasMore {
		result.HasMore = true
		result.ContinuationCursor = nextCursor
		result.ContinuationAvailable = true
	}
	if !result.HasMore {
		result.ContinuationCursor = ""
		result.ContinuationAvailable = false
		return result, nil, "", nil
	}
	if fits, err := r.resultFits(ctx, result); err != nil {
		return SourceReadResult{}, nil, "", err
	} else if !fits {
		return SourceReadResult{}, nil, "", ErrInvalidSourceRead
	}
	return result, &next, nextCursor, nil
}

func (r *SourceRecovery) buildRelevantPage(
	ctx context.Context,
	state sourceRecoveryCursorState,
	messages []conversation.Message,
) (SourceReadResult, *sourceRecoveryCursorState, string, error) {
	if len(messages) != len(state.Sequences) || state.WindowIndex < 0 ||
		state.WindowIndex >= len(state.Windows) {
		return SourceReadResult{}, nil, "", ErrSourceMessagesInvalid
	}
	bySequence := make(map[int64]conversation.Message, len(messages))
	for _, message := range messages {
		bySequence[message.Seq] = message
	}
	result := SourceReadResult{
		Mode:     SourceReadModeRelevant,
		Messages: make([]SourceMessage, 0, min(r.maxMessages, len(state.Windows)-state.WindowIndex)),
	}
	next := state
	nextCursor := uuid.NewString()
	for len(result.Messages) < r.maxMessages && next.WindowIndex < len(next.Windows) {
		window := next.Windows[next.WindowIndex]
		message, ok := bySequence[window.Sequence]
		if !ok {
			return SourceReadResult{}, nil, "", ErrSourceMessagesInvalid
		}
		contentRunes := []rune(message.Content)
		offset := next.WindowOffsetRunes
		if offset == 0 {
			offset = window.StartRunes
		}
		if window.StartRunes < 0 || window.EndRunes > len(contentRunes) ||
			window.EndRunes <= window.StartRunes || offset < window.StartRunes || offset >= window.EndRunes {
			return SourceReadResult{}, nil, "", ErrSourceCursorInvalid
		}
		remaining := contentRunes[offset:window.EndRunes]
		fullWindow := sourceWindowProjection(message, remaining, offset, window, true)
		afterFull := next
		afterFull.WindowIndex++
		afterFull.WindowOffsetRunes = 0
		candidate := appendSourceMessage(
			result, fullWindow, afterFull.WindowIndex < len(afterFull.Windows), nextCursor,
		)
		fits, err := r.resultFits(ctx, candidate)
		if err != nil {
			return SourceReadResult{}, nil, "", err
		}
		if fits {
			result = candidate
			next = afterFull
			continue
		}

		prefixLength, err := r.largestFittingWindowPrefix(
			ctx, result, message, remaining, offset, window, nextCursor,
		)
		if err != nil {
			return SourceReadResult{}, nil, "", err
		}
		if prefixLength == 0 {
			if len(result.Messages) == 0 {
				return SourceReadResult{}, nil, "", ErrInvalidSourceRead
			}
			result.HasMore = true
			result.ContinuationAvailable = true
			result.ContinuationCursor = nextCursor
			break
		}
		windowComplete := prefixLength == len(remaining)
		result.Messages = append(result.Messages, sourceWindowProjection(
			message, remaining[:prefixLength], offset, window, windowComplete,
		))
		if windowComplete {
			next.WindowIndex++
			next.WindowOffsetRunes = 0
		} else {
			next.WindowOffsetRunes = offset + prefixLength
		}
		result.HasMore = next.WindowIndex < len(next.Windows)
		result.ContinuationAvailable = result.HasMore
		result.ContinuationCursor = nextCursor
		break
	}
	if next.WindowIndex < len(next.Windows) && !result.HasMore {
		result.HasMore = true
		result.ContinuationAvailable = true
		result.ContinuationCursor = nextCursor
	}
	if !result.HasMore {
		result.ContinuationAvailable = false
		result.ContinuationCursor = ""
		return result, nil, "", nil
	}
	if fits, err := r.resultFits(ctx, result); err != nil {
		return SourceReadResult{}, nil, "", err
	} else if !fits {
		return SourceReadResult{}, nil, "", ErrInvalidSourceRead
	}
	return result, &next, nextCursor, nil
}

func (r *SourceRecovery) largestFittingWindowPrefix(
	ctx context.Context,
	base SourceReadResult,
	message conversation.Message,
	remaining []rune,
	offset int,
	window sourceRecoveryWindow,
	cursor string,
) (int, error) {
	low, high, best := 1, len(remaining), 0
	for low <= high {
		middle := low + (high-low)/2
		candidate := appendSourceMessage(base, sourceWindowProjection(
			message, remaining[:middle], offset, window, middle == len(remaining),
		), true, cursor)
		fits, err := r.resultFits(ctx, candidate)
		if err != nil {
			return 0, err
		}
		if fits {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best, nil
}

func sourceWindowProjection(
	message conversation.Message,
	content []rune,
	offset int,
	window sourceRecoveryWindow,
	windowComplete bool,
) SourceMessage {
	totalRunes := len([]rune(message.Content))
	return SourceMessage{
		MessageRef: "conversation_message:" + message.ID.String(),
		Seq:        message.Seq, Role: message.Role, Content: string(content),
		ContentOffsetRunes: offset, ContentEndRunes: offset + len(content),
		MessageTotalRunes: totalRunes,
		ContentComplete:   windowComplete && window.StartRunes == 0 && window.EndRunes == totalRunes,
		WindowComplete:    windowComplete, MatchScore: window.Score,
	}
}

func buildRelevantSourceWindows(messages []conversation.Message, query string) []sourceRecoveryWindow {
	terms := sourceQueryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	const (
		blockRunes     = 800
		contextRunes   = 32
		maxWindowRunes = 2400
		maxWindows     = 64
	)
	windows := make([]sourceRecoveryWindow, 0, len(messages))
	for _, message := range messages {
		content := []rune(message.Content)
		for blockStart := 0; blockStart < len(content); blockStart += blockRunes {
			blockEnd := min(blockStart+blockRunes, len(content))
			block := content[blockStart:blockEnd]
			score := sourceWindowScore(block, query, terms)
			if score < 1 {
				continue
			}
			matchOffset := sourceWindowFirstMatch(block, terms)
			if matchOffset < 0 {
				continue
			}
			matchStart := blockStart + matchOffset
			windows = append(windows, sourceRecoveryWindow{
				Sequence:   message.Seq,
				StartRunes: max(0, matchStart-contextRunes),
				EndRunes:   min(len(content), blockEnd+contextRunes), Score: score,
			})
		}
	}
	if len(windows) == 0 {
		return nil
	}
	slices.SortFunc(windows, func(left, right sourceRecoveryWindow) int {
		if left.Sequence != right.Sequence {
			if left.Sequence < right.Sequence {
				return -1
			}
			return 1
		}
		return left.StartRunes - right.StartRunes
	})
	merged := make([]sourceRecoveryWindow, 0, len(windows))
	for _, current := range windows {
		if len(merged) > 0 {
			previous := &merged[len(merged)-1]
			mergedEnd := max(previous.EndRunes, current.EndRunes)
			if previous.Sequence == current.Sequence && current.StartRunes <= previous.EndRunes+64 &&
				mergedEnd-previous.StartRunes <= maxWindowRunes {
				previous.EndRunes = mergedEnd
				previous.Score += current.Score
				continue
			}
		}
		merged = append(merged, current)
	}
	slices.SortFunc(merged, func(left, right sourceRecoveryWindow) int {
		switch {
		case left.Score > right.Score:
			return -1
		case left.Score < right.Score:
			return 1
		case left.Sequence < right.Sequence:
			return -1
		case left.Sequence > right.Sequence:
			return 1
		default:
			return left.StartRunes - right.StartRunes
		}
	})
	if len(merged) > maxWindows {
		merged = merged[:maxWindows]
	}
	return merged
}

func sourceQueryTerms(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	fields := strings.FieldsFunc(query, func(current rune) bool {
		return unicode.IsSpace(current) || unicode.IsPunct(current) || unicode.IsSymbol(current) ||
			current == '的' || current == '和' || current == '与' || current == '或' || current == '及'
	})
	terms := make([]string, 0, min(32, len(fields)*2))
	seen := make(map[string]struct{})
	appendTerm := func(value string) {
		value = strings.TrimSpace(value)
		if len([]rune(value)) < 1 || len([]rune(value)) > 64 || len(terms) >= 32 {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		terms = append(terms, value)
	}
	for _, field := range fields {
		appendTerm(field)
		runes := []rune(field)
		if len(runes) <= 4 || !containsCJK(runes) {
			continue
		}
		for index := 0; index+1 < len(runes) && len(terms) < 32; index++ {
			appendTerm(string(runes[index : index+2]))
		}
	}
	return terms
}

func containsCJK(value []rune) bool {
	for _, current := range value {
		if unicode.Is(unicode.Han, current) || unicode.Is(unicode.Hiragana, current) ||
			unicode.Is(unicode.Katakana, current) || unicode.Is(unicode.Hangul, current) {
			return true
		}
	}
	return false
}

func sourceWindowScore(content []rune, query string, terms []string) int {
	text := strings.ToLower(string(content))
	score := 0
	compactQuery := strings.Join(strings.Fields(strings.ToLower(query)), "")
	compactText := strings.Join(strings.Fields(text), "")
	if len([]rune(compactQuery)) >= 2 && strings.Contains(compactText, compactQuery) {
		score += 100
	}
	for _, term := range terms {
		occurrences := strings.Count(text, term)
		if occurrences > 3 {
			occurrences = 3
		}
		if occurrences > 0 {
			score += occurrences * max(2, len([]rune(term)))
		}
	}
	return score
}

func sourceWindowFirstMatch(content []rune, terms []string) int {
	text := strings.ToLower(string(content))
	firstByte := -1
	for _, term := range terms {
		current := strings.Index(text, term)
		if current >= 0 && (firstByte < 0 || current < firstByte) {
			firstByte = current
		}
	}
	if firstByte < 0 {
		return -1
	}
	return utf8.RuneCountInString(text[:firstByte])
}

func (r *SourceRecovery) largestFittingPrefix(
	ctx context.Context,
	base SourceReadResult,
	message conversation.Message,
	content []rune,
	offset int,
	cursor string,
) (int, error) {
	remaining := content[offset:]
	low, high, best := 1, len(remaining), 0
	for low <= high {
		middle := low + (high-low)/2
		candidate := appendSourceMessage(base, sourceMessageProjection(
			message, remaining[:middle], offset, middle == len(remaining),
		), true, cursor)
		fits, err := r.resultFits(ctx, candidate)
		if err != nil {
			return 0, err
		}
		if fits {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best, nil
}

func (r *SourceRecovery) resultFits(ctx context.Context, result SourceReadResult) (bool, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return false, ErrSourceMessagesInvalid
	}
	tokens, err := r.tokenCounter.Count(ctx, string(encoded))
	if err != nil {
		return false, fmt.Errorf("count conversation memory source tokens: %w", err)
	}
	if tokens < 0 {
		return false, ErrSourceMessagesInvalid
	}
	return tokens <= r.maxTokens, nil
}

func sourceMessageProjection(
	message conversation.Message,
	content []rune,
	offset int,
	complete bool,
) SourceMessage {
	return SourceMessage{
		MessageRef: "conversation_message:" + message.ID.String(),
		Seq:        message.Seq, Role: message.Role, Content: string(content),
		ContentOffsetRunes: offset, ContentEndRunes: offset + len(content),
		MessageTotalRunes: len([]rune(message.Content)), ContentComplete: complete,
		WindowComplete: complete,
	}
}

func appendSourceMessage(
	base SourceReadResult,
	message SourceMessage,
	hasMore bool,
	cursor string,
) SourceReadResult {
	result := base
	result.Messages = append([]SourceMessage(nil), base.Messages...)
	result.HasMore = hasMore
	result.ContinuationAvailable = hasMore
	result.Messages = append(result.Messages, message)
	if hasMore {
		result.ContinuationCursor = cursor
	}
	return result
}
