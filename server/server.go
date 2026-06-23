/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"github.com/hertz-contrib/sse"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	commontool "github.com/yuterigele/openbook/internal/einocommon/tool"
	"github.com/yuterigele/openbook/a2ui"
	"github.com/yuterigele/openbook/mem"
	"github.com/yuterigele/openbook/msgops"
	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/tools"
	"github.com/yuterigele/openbook/wecom"
)

func init() {
	schema.RegisterName[ChatItem]("chatwitheino_chat_item")
	schema.RegisterName[commontool.ApprovalResult]("chatwitheino_approval_result")
}

// ChatItem is the item type for TurnLoop. Each user query or approval decision
// is pushed as a ChatItem.
type ChatItem struct {
	Query          string                     // user message text (empty for approval items)
	ApprovalResult *commontool.ApprovalResult // non-nil when this item carries an approval decision
	InterruptID    string                     // which interrupt this approval resolves
}

// errInterrupted is returned by OnAgentEvents when the agent is interrupted
// for approval. The TurnLoop exits with this as ExitReason.
var errInterrupted = errors.New("agent interrupted for approval")

// Config holds all dependencies for the HTTP server.
type Config[M adk.MessageType] struct {
	Agent           adk.TypedAgent[M]
	ChatModel       einomodel.BaseModel[M]
	CheckPointStore adk.CheckPointStore
	Store           *mem.Store[M]
	WorkspaceDir    string
	ProjectRoot     string // root of the codebase the agent can explore
	ExamplesDir     string // root of the eino-examples repo (for example searches)
	Port            string
	// 企业微信配置（多店版：Router 优先；WeComConfig 作为 fallback）
	WeComConfig *wecom.Config
	WeComRouter *wecom.Router
}

// Server wraps a Hertz HTTP server with the chat-with-doc routes.
type Server[M adk.MessageType] struct {
	cfg        Config[M]
	turnStates sync.Map // sessionID → *sessionTurnState
	// 微信客服消息追踪器（cursor分页 + msgid去重）
	kfTracker sync.Map // openKfID → *kfMessageTracker
	h         *hserver.Hertz // 暴露给 main 注册额外路由（商户后台 / API）
}

// Hertz 返回内部 *hertz.Hertz 实例，供外部注册路由用（PRD §11.2 商户后台 + API）
func (s *Server[M]) Hertz() *hserver.Hertz { return s.h }

// New creates a Server from the given config.
func New[M adk.MessageType](cfg Config[M]) *Server[M] {
	cfg.CheckPointStore = withDeleteCheckpointStore(cfg.CheckPointStore)
	return &Server[M]{cfg: cfg}
}

// EnsureHertz 在 Spin 之前调用，确保 Hertz 实例已创建并允许外部注册路由（PRD §11.2）
// 多次调用幂等。
func (s *Server[M]) EnsureHertz() *hserver.Hertz {
	if s.h == nil {
		s.h = hserver.Default(hserver.WithHostPorts(":" + s.cfg.Port))
	}
	return s.h
}

type deleteCheckpointStore struct {
	mu         sync.Mutex
	inner      adk.CheckPointStore
	tombstones map[string]struct{}
}

func withDeleteCheckpointStore(store adk.CheckPointStore) adk.CheckPointStore {
	if store == nil {
		return nil
	}
	return &deleteCheckpointStore{
		inner:      store,
		tombstones: map[string]struct{}{},
	}
}

func (s *deleteCheckpointStore) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, deleted := s.tombstones[checkPointID]; deleted {
		return nil, false, nil
	}
	return s.inner.Get(ctx, checkPointID)
}

func (s *deleteCheckpointStore) Set(ctx context.Context, checkPointID string, checkPoint []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tombstones, checkPointID)
	return s.inner.Set(ctx, checkPointID, checkPoint)
}

func (s *deleteCheckpointStore) Delete(ctx context.Context, checkPointID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if deleter, ok := s.inner.(adk.CheckPointDeleter); ok {
		return deleter.Delete(ctx, checkPointID)
	}
	s.tombstones[checkPointID] = struct{}{}
	return nil
}

// iterEnvelope carries the event iterator from OnAgentEvents to the HTTP handler.
// The done channel is included so the handler always sends results back to the
// correct OnAgentEvents invocation, even if a preempt replaces the session channels.
type iterEnvelope[M adk.MessageType] struct {
	events  *adk.AsyncIterator[*adk.TypedAgentEvent[M]]
	history []M
	done    chan iterResult[M]
}

// iterResult carries the outcome from the HTTP handler back to OnAgentEvents.
type iterResult[M adk.MessageType] struct {
	lastContent   string
	intermediates []M // tool call + tool result messages to persist
	interruptID   string
	msgIdx        int
	err           error
}

// sessionTurnState holds the TurnLoop and event bridge channels for a session.
type sessionTurnState[M adk.MessageType] struct {
	mu          sync.Mutex
	loop        *adk.TurnLoop[*ChatItem, M]
	iterReady   chan iterEnvelope[M] // OnAgentEvents → HTTP handler
	iterDone    chan iterResult[M]   // HTTP handler → OnAgentEvents
	handlerDone chan struct{}        // closed to tell a prev handler to bail on preempt
}

func (s *Server[M]) getTurnState(sessionID string) *sessionTurnState[M] {
	val, _ := s.turnStates.LoadOrStore(sessionID, &sessionTurnState[M]{})
	return val.(*sessionTurnState[M])
}

// startLoopCleanup spawns a goroutine that waits for the loop to exit
// (e.g. due to an error or all items consumed) and nils out ts.loop so
// the next handleChat creates a fresh loop instead of trying to preempt
// a dead one.
func (s *Server[M]) startLoopCleanup(ts *sessionTurnState[M], loop *adk.TurnLoop[*ChatItem, M], sessionID string) {
	go func() {
		result := loop.Wait()
		ts.mu.Lock()
		if ts.loop == loop {
			ts.loop = nil
		}
		ts.mu.Unlock()
		if result.ExitReason != nil {
			log.Printf("[loop] session=%s exited with error: %v", sessionID, result.ExitReason)
		} else {
			log.Printf("[loop] session=%s exited cleanly", sessionID)
		}
	}()
}

	// Spin starts the HTTP server (blocking).
	func (s *Server[M]) Spin() {
		h := s.EnsureHertz()

		// 全局中间件：记录所有请求，用于排查回调是否到达
		h.Use(func(ctx context.Context, c *app.RequestContext) {
			log.Printf("[http] %s %s query=%s contentType=%s contentLength=%d",
				c.Method(), c.Path(), c.QueryArgs().String(), c.GetHeader("Content-Type"), len(c.Request.Body()))
			c.Next(ctx)
		})

	h.GET("/", func(ctx context.Context, c *app.RequestContext) {
		data, err := os.ReadFile("static/index.html")
		if err != nil {
			c.JSON(consts.StatusNotFound, map[string]string{"error": "index.html not found"})
			return
		}
		c.Data(consts.StatusOK, "text/html; charset=utf-8", data)
	})

	h.POST("/sessions", func(ctx context.Context, c *app.RequestContext) {
		id := uuid.New().String()
		if _, err := s.cfg.Store.GetOrCreate(id); err != nil {
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		c.JSON(consts.StatusOK, map[string]string{"id": id})
	})

	h.GET("/sessions", func(ctx context.Context, c *app.RequestContext) {
		metas, err := s.cfg.Store.List()
		if err != nil {
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if metas == nil {
			metas = []mem.SessionMeta{}
		}
		c.JSON(consts.StatusOK, metas)
	})

	h.DELETE("/sessions/:id", func(ctx context.Context, c *app.RequestContext) {
		id := c.Param("id")
		// Stop any running loop for this session.
		ts := s.getTurnState(id)
		ts.mu.Lock()
		if ts.loop != nil {
			ts.loop.Stop(adk.WithImmediate())
			ts.loop = nil
		}
		ts.mu.Unlock()

		if err := s.cfg.Store.Delete(id); err != nil {
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.turnStates.Delete(id)
		c.Status(consts.StatusNoContent)
	})

	h.POST("/sessions/:id/chat", func(ctx context.Context, c *app.RequestContext) {
		s.handleChat(ctx, c)
	})

	h.GET("/sessions/:id/render", func(ctx context.Context, c *app.RequestContext) {
		s.handleRender(ctx, c)
	})

	h.POST("/sessions/:id/approve", func(ctx context.Context, c *app.RequestContext) {
		s.handleApprove(ctx, c)
	})

	h.POST("/sessions/:id/abort", func(ctx context.Context, c *app.RequestContext) {
		s.handleAbort(ctx, c)
	})

	h.POST("/sessions/:id/docs", func(ctx context.Context, c *app.RequestContext) {
		s.handleUpload(ctx, c)
	})

	// 企业微信回调接口
	if s.cfg.WeComConfig != nil && s.cfg.WeComConfig.CorpID != "" {
		s.registerWeComCallback(h)
	}

	h.Spin()
}

type chatRequest struct {
	Message string `json:"message"`
}

type approveRequest struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

func (s *Server[M]) handleRender(_ context.Context, c *app.RequestContext) {
	id := c.Param("id")
	sess, err := s.cfg.Store.GetOrCreate(id)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var buf bytes.Buffer
	if err := a2ui.RenderHistory(&buf, id, sess.GetMessages()); err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.Data(consts.StatusOK, "application/x-ndjson", buf.Bytes())
}

// handleChat handles a new chat message. It creates or reuses a TurnLoop for the session.
// If a loop is already running (busy), it pushes with preempt to cancel the current turn.
func (s *Server[M]) handleChat(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")

	body, _ := c.Body()
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Message == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}

	log.Printf("[chat] session=%s msg=%q", id, req.Message)

	sess, err := s.cfg.Store.GetOrCreate(id)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	item := &ChatItem{Query: req.Message}

	ts := s.getTurnState(id)

	// Each handler gets its own local iterReady channel reference and a
	// handlerDone channel. This avoids races when multiple preempts replace
	// the channels on ts concurrently.
	var localIterReady chan iterEnvelope[M]
	var localHandlerDone chan struct{}

	ts.mu.Lock()
	if ts.loop != nil {
		// Loop exists — try to push with preempt (AfterToolCalls).
		loop := ts.loop
		log.Printf("[chat] session=%s preempting current turn", id)
		// Signal any previous handler waiting on iterReady to bail.
		if ts.handlerDone != nil {
			close(ts.handlerDone)
		}
		ts.iterReady = make(chan iterEnvelope[M], 1)
		ts.iterDone = make(chan iterResult[M], 1)
		ts.handlerDone = make(chan struct{})
		localIterReady = ts.iterReady
		localHandlerDone = ts.handlerDone
		ts.mu.Unlock()
		ok, _ := loop.Push(item, adk.WithPreempt[*ChatItem, M](adk.AfterToolCalls))
		if !ok {
			// Loop already stopped (e.g. error on previous turn) — create new one.
			log.Printf("[chat] session=%s loop was dead, creating new loop", id)
			ts.mu.Lock()
			loop = s.newLoop(sess, id, false)
			ts.loop = loop
			ts.iterReady = make(chan iterEnvelope[M], 1)
			ts.iterDone = make(chan iterResult[M], 1)
			ts.handlerDone = make(chan struct{})
			localIterReady = ts.iterReady
			localHandlerDone = ts.handlerDone
			ts.mu.Unlock()
			loop.Push(item)
			loop.Run(context.Background())
			s.startLoopCleanup(ts, loop, id)
		}
	} else {
		// No loop — create a new one.
		loop := s.newLoop(sess, id, false)
		ts.loop = loop
		ts.iterReady = make(chan iterEnvelope[M], 1)
		ts.iterDone = make(chan iterResult[M], 1)
		ts.handlerDone = make(chan struct{})
		localIterReady = ts.iterReady
		localHandlerDone = ts.handlerDone
		ts.mu.Unlock()
		loop.Push(item)
		loop.Run(context.Background())
		s.startLoopCleanup(ts, loop, id)
	}

	// User message is persisted in GenInput (not here) to guarantee correct
	// session history ordering: the preempted turn's intermediates are persisted
	// by OnAgentEvents before GenInput fires for the new turn.

	// Open SSE stream and start keepalives BEFORE waiting for the iterator.
	// During a preempt the old turn may take tens of seconds to drain; if we
	// don't write anything the browser/TCP stack may consider the connection
	// dead, causing all subsequent writes to fail silently.
	stream := sse.NewStream(c)
	defer func() { _ = c.Flush() }()

	kaStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-kaStop:
				return
			case <-ticker.C:
				_ = stream.Publish(&sse.Event{Data: []byte{}})
			}
		}
	}()

	// Wait for OnAgentEvents to send us the iterator. Use local channel
	// references so a concurrent preempt replacing ts.iterReady doesn't
	// orphan us on a stale channel.
	var envelope iterEnvelope[M]
	select {
	case envelope = <-localIterReady:
	case <-localHandlerDone:
		// Another preempt took over — our turn was superseded.
		close(kaStop)
		log.Printf("[chat] session=%s handler superseded by newer preempt", id)
		_ = stream.Publish(&sse.Event{Data: []byte(`{"event":"preempted"}`)})
		return
	case <-time.After(60 * time.Second):
		close(kaStop)
		// Stream is already open; send an error event instead of JSON.
		_ = stream.Publish(&sse.Event{Data: []byte(`{"error":"agent did not start in time"}`)})
		return
	}

	lastContent, intermediates, interruptID, finalMsgIdx, streamErr := a2ui.StreamToWriter(
		&sseLineWriter{stream: stream}, id, envelope.history, envelope.events,
	)
	close(kaStop)

	// Send result back to the SAME OnAgentEvents that sent us this envelope.
	envelope.done <- iterResult[M]{
		lastContent:   lastContent,
		intermediates: intermediates,
		interruptID:   interruptID,
		msgIdx:        finalMsgIdx,
		err:           streamErr,
	}

	if streamErr != nil {
		log.Printf("[chat] session=%s stream error: %v", id, streamErr)
	} else if interruptID != "" {
		log.Printf("[chat] session=%s interrupted: id=%s", id, interruptID)
	} else {
		log.Printf("[chat] session=%s done, response=%d chars", id, len(lastContent))
	}
}

// handleApprove resumes an interrupted agent run with the user's approval decision.
// Creates a new TurnLoop with checkpoint/resume to continue from the interrupt.
func (s *Server[M]) handleApprove(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")

	sess, err := s.cfg.Store.GetOrCreate(id)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	interruptID := sess.GetPendingInterruptID()
	if interruptID == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "no pending interrupt for this session"})
		return
	}

	body, _ := c.Body()
	var req approveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	var reason *string
	if req.Reason != "" {
		reason = &req.Reason
	}
	result := &commontool.ApprovalResult{Approved: req.Approved, DisapproveReason: reason}

	// Clear the pending interrupt so a double-approve returns 400.
	sess.SetPendingInterruptID("")

	log.Printf("[approve] session=%s interruptID=%s approved=%v", id, interruptID, req.Approved)

	// Create a new loop with checkpoint resume.
	ts := s.getTurnState(id)
	ts.mu.Lock()
	// Clear any old loop.
	if ts.loop != nil {
		ts.loop.Stop(adk.WithImmediate())
	}
	// Signal any previous handler to bail.
	if ts.handlerDone != nil {
		close(ts.handlerDone)
	}
	loop := s.newLoop(sess, id, true)
	ts.loop = loop
	ts.iterReady = make(chan iterEnvelope[M], 1)
	ts.iterDone = make(chan iterResult[M], 1)
	ts.handlerDone = make(chan struct{})
	localIterReady := ts.iterReady
	localHandlerDone := ts.handlerDone
	ts.mu.Unlock()

	// Push the approval item before starting.
	loop.Push(&ChatItem{
		ApprovalResult: result,
		InterruptID:    interruptID,
	})
	loop.Run(context.Background())
	s.startLoopCleanup(ts, loop, id)

	// Open SSE stream and start keepalives before waiting.
	stream := sse.NewStream(c)
	defer func() { _ = c.Flush() }()

	kaStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-kaStop:
				return
			case <-ticker.C:
				_ = stream.Publish(&sse.Event{Data: []byte{}})
			}
		}
	}()

	// Wait for OnAgentEvents to send us the iterator.
	var envelope iterEnvelope[M]
	select {
	case envelope = <-localIterReady:
	case <-localHandlerDone:
		close(kaStop)
		log.Printf("[approve] session=%s handler superseded by newer request", id)
		_ = stream.Publish(&sse.Event{Data: []byte(`{"event":"preempted"}`)})
		return
	case <-time.After(60 * time.Second):
		close(kaStop)
		_ = stream.Publish(&sse.Event{Data: []byte(`{"error":"agent did not start in time"}`)})
		return
	}
	_ = envelope.history // not used for StreamContinue

	lastContent, newInterruptID, finalMsgIdx, streamErr := a2ui.StreamContinue(
		&sseLineWriter{stream: stream}, id, sess.GetMsgIdx(), envelope.events,
	)
	close(kaStop)

	// Send result back to the SAME OnAgentEvents that sent us this envelope.
	envelope.done <- iterResult[M]{
		lastContent: lastContent,
		interruptID: newInterruptID,
		msgIdx:      finalMsgIdx,
		err:         streamErr,
	}

	if streamErr != nil {
		log.Printf("[approve] session=%s stream error: %v", id, streamErr)
	} else if newInterruptID != "" {
		log.Printf("[approve] session=%s re-interrupted: id=%s", id, newInterruptID)
	} else {
		log.Printf("[approve] session=%s done, response=%d chars", id, len(lastContent))
	}
}

// handleAbort immediately stops the current TurnLoop for a session.
func (s *Server[M]) handleAbort(_ context.Context, c *app.RequestContext) {
	id := c.Param("id")

	ts := s.getTurnState(id)
	ts.mu.Lock()
	loop := ts.loop
	ts.loop = nil
	ts.mu.Unlock()

	if loop == nil {
		c.JSON(consts.StatusOK, map[string]string{"status": "no active loop"})
		return
	}

	log.Printf("[abort] session=%s stopping loop immediately", id)
	loop.Stop(adk.WithImmediate())
	loop.Wait()
	log.Printf("[abort] session=%s loop stopped", id)

	c.JSON(consts.StatusOK, map[string]string{"status": "aborted"})
}

// newLoop creates a new TurnLoop for the session. Every loop uses the checkpoint
// store when one is configured so the first /chat interrupt can be persisted
// and the later /approve loop can resume it.
func (s *Server[M]) newLoop(sess *mem.Session[M], sessionID string, withResume bool) *adk.TurnLoop[*ChatItem, M] {
	_ = withResume
	cfg := adk.TurnLoopConfig[*ChatItem, M]{
		GenInput:      s.makeGenInput(sess, sessionID),
		PrepareAgent:  s.makePrepareAgent(),
		OnAgentEvents: s.makeOnAgentEvents(sess, sessionID),
	}
	if s.cfg.CheckPointStore != nil {
		cfg.Store = s.cfg.CheckPointStore
		cfg.CheckpointID = sessionID
		cfg.GenResume = s.makeGenResume()
	}
	return adk.NewTurnLoop(cfg)
}

// makeGenInput returns the GenInput callback. It builds agent messages from
// session history + workspace context.
func (s *Server[M]) makeGenInput(sess *mem.Session[M], sessionID string) func(ctx context.Context, loop *adk.TurnLoop[*ChatItem, M], items []*ChatItem) (*adk.GenInputResult[*ChatItem, M], error) {
	return func(ctx context.Context, loop *adk.TurnLoop[*ChatItem, M], items []*ChatItem) (*adk.GenInputResult[*ChatItem, M], error) {
		// Find the first item with a query.
		var consumed []*ChatItem
		var remaining []*ChatItem
		var queryItem *ChatItem
		for _, item := range items {
			if queryItem == nil && item.Query != "" {
				queryItem = item
				consumed = append(consumed, item)
			} else {
				remaining = append(remaining, item)
			}
		}
		if queryItem == nil {
			// No query items — stop the loop.
			loop.Stop(adk.WithStopCause("no query items"))
			return &adk.GenInputResult[*ChatItem, M]{
				Input:     &adk.TypedAgentInput[M]{Messages: []M{msgops.NewUser[M]("done")}},
				Remaining: items,
			}, nil
		}

		// Persist the user message NOW — GenInput fires only after any previous
		// turn's OnAgentEvents has finished persisting its intermediates, so the
		// session history order is guaranteed correct.
		userMsg := msgops.NewUser[M](queryItem.Query)
		if appendErr := sess.Append(userMsg); appendErr != nil {
			log.Printf("warn: failed to persist user message: %v", appendErr)
		}

		history := sess.GetMessages()
		runMessages := s.buildRunMessages(sessionID, history)

		log.Printf("[genInput] session=%s query=%q messages=%d", sessionID, queryItem.Query, len(runMessages))

		return &adk.GenInputResult[*ChatItem, M]{
			Input: &adk.TypedAgentInput[M]{
				Messages:        runMessages,
				EnableStreaming: true,
			},
			Consumed:  consumed,
			Remaining: remaining,
		}, nil
	}
}

// makePrepareAgent returns the PrepareAgent callback — returns the same agent.
func (s *Server[M]) makePrepareAgent() func(ctx context.Context, loop *adk.TurnLoop[*ChatItem, M], consumed []*ChatItem) (adk.TypedAgent[M], error) {
	return func(ctx context.Context, loop *adk.TurnLoop[*ChatItem, M], consumed []*ChatItem) (adk.TypedAgent[M], error) {
		return s.cfg.Agent, nil
	}
}

// makeOnAgentEvents returns the OnAgentEvents callback — the bridge between
// the TurnLoop and the HTTP handler.
func (s *Server[M]) makeOnAgentEvents(sess *mem.Session[M], sessionID string) func(ctx context.Context, tc *adk.TurnContext[*ChatItem, M], events *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) error {
	return func(ctx context.Context, tc *adk.TurnContext[*ChatItem, M], events *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) error {
		ts := s.getTurnState(sessionID)

		history := sess.GetMessages()

		// Snapshot bridge channels under lock to avoid races with handleChat
		// which may recreate them for a preempt.
		ts.mu.Lock()
		ready := ts.iterReady
		done := ts.iterDone
		ts.mu.Unlock()

		// Send the iterator to the HTTP handler. Include the done channel
		// so the handler replies to THIS invocation, not a future one.
		select {
		case ready <- iterEnvelope[M]{events: events, history: history, done: done}:
		case <-ctx.Done():
			return ctx.Err()
		}

		// Wait for the HTTP handler to finish draining. Also select on ctx.Done
		// to avoid hanging when a preempt supersedes the handler — in that case
		// the old handler bails via handlerDone and nobody sends to our done channel.
		var result iterResult[M]
		select {
		case result = <-done:
		case <-ctx.Done():
			return ctx.Err()
		}

		// Persist all intermediate messages (assistant text+tool calls, tool results).
		// The intermediates already include the final assistant text message if any,
		// so we don't need to persist lastContent separately.
		for _, msg := range result.intermediates {
			if appendErr := sess.Append(msg); appendErr != nil {
				log.Printf("warn: failed to persist intermediate message: %v", appendErr)
			}
		}
		if result.interruptID != "" {
			sess.SetPendingInterruptID(result.interruptID)
			sess.SetMsgIdx(result.msgIdx)
			return errInterrupted
		}
		return result.err
	}
}

// makeGenResume returns the GenResume callback for interrupt/resume.
func (s *Server[M]) makeGenResume() func(ctx context.Context, loop *adk.TurnLoop[*ChatItem, M], canceledItems, unhandledItems, newItems []*ChatItem) (*adk.GenResumeResult[*ChatItem, M], error) {
	return func(ctx context.Context, loop *adk.TurnLoop[*ChatItem, M], canceledItems, unhandledItems, newItems []*ChatItem) (*adk.GenResumeResult[*ChatItem, M], error) {
		// Find the approval item in newItems.
		var approvalItem *ChatItem
		for _, item := range newItems {
			if item.ApprovalResult != nil {
				approvalItem = item
				break
			}
		}
		if approvalItem == nil {
			return nil, errors.New("no approval item found for resume")
		}

		return &adk.GenResumeResult[*ChatItem, M]{
			ResumeParams: &adk.ResumeParams{
				Targets: map[string]any{approvalItem.InterruptID: approvalItem.ApprovalResult},
			},
			Consumed:  canceledItems,
			Remaining: unhandledItems,
		}, nil
	}
}

// buildRunMessages prepends a context message so the agent knows about the
// project root and the session workspace. This message is never stored in history.
func (s *Server[M]) buildRunMessages(sessionID string, history []M) []M {
	var lines []string
	lines = append(lines, "[Context]")
	lines = append(lines,
		"IMPORTANT RULES:",
		"  1. Always use filesystem tools to look up real code before answering. Do not guess or make up information.",
		"  2. After using tools (even if they return no results), you MUST write a text response to the user summarizing what you found.",
		"  3. Never end your turn without a text response — tool calls alone are not sufficient.",
		"  4. When asked to build or test code, use the execute tool to run the command.",
		"     Each Go example has its own go.mod. To build an example, run:",
		"       cd <example-dir> && go build ./...",
		"     NEVER assume a build succeeded without actually running it.",
		"  5. When writing or editing a file and then claiming it compiles, you MUST run the build tool to verify.",
	)

	if s.cfg.ProjectRoot != "" {
		lines = append(lines,
			fmt.Sprintf("Project root: %s", s.cfg.ProjectRoot),
			"  IMPORTANT: Always pass the project root as the path argument when using filesystem tools.",
			fmt.Sprintf("  - grep(pattern=\"...\", path=\"%s\")", s.cfg.ProjectRoot),
			fmt.Sprintf("  - glob(pattern=\"%s/**/*.go\")", s.cfg.ProjectRoot),
			fmt.Sprintf("  - read_file(file_path=\"%s/some/file.go\")", s.cfg.ProjectRoot),
			"  grep and glob recurse into ALL subdirectories under the given path.",
			"  Top-level subdirectories of the project root:",
		)
		if entries, err := os.ReadDir(s.cfg.ProjectRoot); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					lines = append(lines, "    - "+filepath.Join(s.cfg.ProjectRoot, e.Name())+"/")
				}
			}
		}
		lines = append(lines, "  Use these tools to read actual source code before answering questions about the codebase.")
	}

	if s.cfg.ExamplesDir != "" && s.cfg.ExamplesDir != s.cfg.ProjectRoot {
		lines = append(lines,
			fmt.Sprintf("eino-examples directory: %s", s.cfg.ExamplesDir),
			"  When the user asks about examples or sample code, search here specifically:",
			fmt.Sprintf("  - grep(pattern=\"...\", path=\"%s\")", s.cfg.ExamplesDir),
			fmt.Sprintf("  - glob(pattern=\"%s/**/*.go\")", s.cfg.ExamplesDir),
		)
	}

	absWorkDir, err := filepath.Abs(filepath.Join(s.cfg.WorkspaceDir, sessionID))
	if err == nil {
		entries, _ := os.ReadDir(absWorkDir)
		var uploadedFiles []string
		for _, e := range entries {
			if !e.IsDir() {
				uploadedFiles = append(uploadedFiles, filepath.Join(absWorkDir, e.Name()))
			}
		}
		if len(uploadedFiles) > 0 {
			lines = append(lines,
				fmt.Sprintf("Session workspace: %s", absWorkDir),
				"  Uploaded files:",
			)
			for _, f := range uploadedFiles {
				lines = append(lines, "    - "+f)
			}
		}
	}

	ctx := strings.Join(lines, "\n")
	runMessages := make([]M, 0, len(history)+1)
	runMessages = append(runMessages, msgops.NewUser[M](ctx))
	runMessages = append(runMessages, msgops.NormalizeMessagesForModelInput(history)...)
	return runMessages
}

func (s *Server[M]) handleUpload(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")

	absWorkDir, err := filepath.Abs(filepath.Join(s.cfg.WorkspaceDir, id))
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(absWorkDir, 0o755); err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "file field is required"})
		return
	}

	dst := filepath.Join(absWorkDir, filepath.Base(fileHeader.Filename))
	if err := c.SaveUploadedFile(fileHeader, dst); err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	c.JSON(consts.StatusOK, map[string]string{
		"name": fileHeader.Filename,
		"path": dst,
	})
}

// sseLineWriter implements io.Writer, buffering until a newline is found,
// then publishing each complete line as an SSE event (without the trailing newline).
type sseLineWriter struct {
	stream *sse.Stream
	buf    []byte
}

func (w *sseLineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := -1
		for i, b := range w.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := w.buf[:idx]
		w.buf = w.buf[idx+1:]
		if len(line) == 0 {
			continue
		}
		if err := w.stream.Publish(&sse.Event{Data: line}); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// fixWeComBody 修复企业微信body中可能被URL解码的+号
// Hertz框架可能对body做了URL解码，导致base64中的+号变成空格
func fixWeComBody(body string) string {
	// 找到 Encrypt CDATA 的位置
	startTag := "<Encrypt><![CDATA["
	endTag := "]]></Encrypt>"
	startIdx := strings.Index(body, startTag)
	if startIdx == -1 {
		return body
	}
	startIdx += len(startTag)
	endIdx := strings.Index(body[startIdx:], endTag)
	if endIdx == -1 {
		return body
	}

	// 提取 Encrypt 值，将空格替换回+
	encryptValue := body[startIdx : startIdx+endIdx]
	fixedEncrypt := strings.ReplaceAll(encryptValue, " ", "+")

	// 重新拼接body
	return body[:startIdx] + fixedEncrypt + body[startIdx+endIdx:]
}

// registerWeComCallback 注册企业微信回调接口（多店版）
//
// 关键变化：
//   - 不再 hardcode 单 corpID 的 crypto/client；按消息的 ToUserName（= CorpID）反查 Router
//   - 找不到 corpID 时 fallback 到 WeComConfig（旧单 corpID 部署兼容）
//   - URL 验证回调（GET）：支持 ?corp_id=xxx 多 corpID 路由；不传 corp_id 时遍历所有 router 里的 corpID
//   - 测试接口 /wecom/test/send 接受 ?shop_id=xxx 参数，注入对应店铺的 shop_id 给 Agent
func (s *Server[M]) registerWeComCallback(h *hserver.Hertz) {
	if s.cfg.WeComRouter == nil {
		log.Printf("[wecom] Router 未配置，回调注册失败")
		return
	}

	router := s.cfg.WeComRouter
log.Printf("[wecom] 多店 router 已注册，已加载 %d 个店铺的加密实例", router.Count())

	// lookupCorpID 从 ShopCrypto 反查 corpID（用于判断 ToUserName 是不是企业应用消息）
	//
	// ShopCrypto 自带 CorpID 字段（Register / SetFallback 时填好），优先用它。
	// LookupCorpIDByPtr 是 fallback 兜底（O(n)）。
	lookupCorpID := func(sc *wecom.ShopCrypto) string {
		if sc == nil {
			return ""
		}
		if sc.CorpID != "" {
			return sc.CorpID
		}
		if s.cfg.WeComRouter != nil {
			return s.cfg.WeComRouter.LookupCorpIDByPtr(sc)
		}
		return ""
	}

	h.GET("/wecom/callback", func(ctx context.Context, c *app.RequestContext) {
		echostr := c.Query("echostr")
		if echostr == "" {
			c.Data(consts.StatusOK, "text/plain", []byte("success"))
			return
		}

		// 选 corpID：query ?corp_id= 优先；不传则用 fallback 单 corpID
		corpID := c.Query("corp_id")
		if corpID == "" && s.cfg.WeComConfig != nil {
			corpID = s.cfg.WeComConfig.CorpID
		}
		if corpID == "" {
			log.Printf("[wecom] URL验证：未指定 corp_id 且无 fallback")
			c.Data(consts.StatusBadRequest, "text/plain", []byte("error"))
			return
		}
		sc, ok := router.Lookup(corpID)
		if !ok {
			log.Printf("[wecom] URL验证：corpID=%s 未在 router 注册", corpID)
			c.Data(consts.StatusBadRequest, "text/plain", []byte("error"))
			return
		}

		msgSignature := c.Query("msg_signature")
		timestamp := c.Query("timestamp")
		nonce := c.Query("nonce")
		plaintext, err := sc.Crypto.VerifyURL(msgSignature, timestamp, nonce, echostr)
		if err != nil {
			log.Printf("[wecom] URL验证失败 corpID=%s: %v", corpID, err)
			c.Data(consts.StatusBadRequest, "text/plain", []byte("error"))
			return
		}
		plaintext = strings.TrimSpace(plaintext)
		plaintext = strings.TrimPrefix(plaintext, "\ufeff")
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Data(consts.StatusOK, "text/plain", []byte(plaintext))
	})

h.POST("/wecom/callback", func(ctx context.Context, c *app.RequestContext) {
		msgSignature := c.Query("msg_signature")
		timestamp := c.Query("timestamp")
		nonce := c.Query("nonce")

		log.Printf("[wecom] 参数: msg_signature=%s timestamp=%s nonce=%s", msgSignature, timestamp, nonce)

		// 先按 msg_signature + timestamp + nonce 解密需要一个 corpID 试。
		// 策略：先用 fallback corpID 试；如果 router 里只有一个 corpID，直接用它；
		// 多个 corpID 时按 URL 上的 ?corp_id= 锁定。
		corpIDHint := c.Query("corp_id")
		var sc *wecom.ShopCrypto
		var err error
		var plaintext string

		if corpIDHint != "" {
			// 路径上指定了 corpID
			sc, _ = router.Lookup(corpIDHint)
			if sc == nil {
				log.Printf("[wecom] 路径 corp_id=%s 未在 router 注册", corpIDHint)
				c.Data(consts.StatusOK, "text/plain", []byte("success"))
				return
			}
			plaintext, err = sc.Crypto.DecryptMsg(msgSignature, timestamp, nonce, c.GetRawData())
		} else {
			// 遍历 router 里的所有 corpID 尝试解密（多 corpID 但 URL 不区分的场景）
			for _, trySc := range router.AllShops() {
				pt, tryErr := trySc.Crypto.DecryptMsg(msgSignature, timestamp, nonce, c.GetRawData())
				if tryErr == nil {
					plaintext = pt
					sc = trySc
					break
				}
			}
			if sc == nil {
				log.Printf("[wecom] 解密消息失败（router 里所有 corpID 都试过）")
				c.Data(consts.StatusOK, "text/plain", []byte("success"))
				return
			}
		}
		if err != nil {
			log.Printf("[wecom] 解密消息失败: %v", err)
			c.Data(consts.StatusOK, "text/plain", []byte("success"))
			return
		}
		_ = err

		// 解析消息
		msg, err := wecom.ParseMessage(plaintext)
		if err != nil {
			log.Printf("解析消息失败: %v", err)
			c.Data(consts.StatusOK, "text/plain", []byte("success"))
			return
		}

		// MsgId 幂等去重（PRD §11.1 P0：防微信回调重试导致重复处理）
		first, dedupErr := wecom.MarkMessageProcessed(ctx, msg)
		if dedupErr != nil {
			log.Printf("[wecom] 幂等去重失败（不阻断流程）: %v", dedupErr)
		} else if !first {
			log.Printf("[wecom] 重复消息跳过: msgID=%d event=%s", msg.MsgId, msg.Event)
			c.Data(consts.StatusOK, "text/plain", []byte("success"))
			return
		}

		log.Printf("[wecom] 收到消息: from=%s type=%s content=%s event=%s shopID=%s corpID=%s",
			msg.FromUserName, msg.MsgType, msg.Content, msg.Event, sc.ShopID, lookupCorpID(sc))

		// 处理文本消息（按 ToUserName 区分企业应用 vs 外部联系人）
		if msg.MsgType == "text" {
			// 企业应用消息：ToUserName == CorpID
			if msg.ToUserName == lookupCorpID(sc) {
				// 企业应用文本消息
				go s.handleWeComMessage(ctx, sc.Client, msg, sc.ShopID, timestamp, nonce, c)
			} else {
				// 外部联系人文本消息
				go s.handleExternalContactMessage(ctx, sc.Client, msg, sc.ShopID)
			}
		}

		// 处理微信客服事件
		if msg.MsgType == "event" && msg.Event == "kf_msg_or_event" {
			go s.handleKfCallback(ctx, sc.Client, msg, sc.ShopID)
		}

		// 处理外部联系人添加事件
		if msg.MsgType == "event" && msg.Event == "change_external_contact" &&
			(msg.ChangeType == "add_external_contact" || msg.ChangeType == "add_half_external_contact") {
			go s.handleAddExternalContact(ctx, sc.Client, msg, sc.ShopID)
		}

		c.Data(consts.StatusOK, "text/plain", []byte("success"))
	})

		// 生成「联系我」二维码接口
		// 员工个人生成的二维码不会触发回调，必须使用此 API 生成官方二维码。
		// 返回二维码图片 URL，客户扫码发消息时企业微信会将消息推送到 /wecom/callback。
		//
		// 使用方式：curl http://localhost:38080/wecom/contact-qrcode?user_id=ZhangSan
		// 可选参数 is_temp=1（临时会话，未认证企业必须用 1）默认为 1
		// 返回：{"qr_code": "https://..."}，将二维码 URL 生成图片后展示给客户扫码。
		h.GET("/wecom/contact-qrcode", func(ctx context.Context, c *app.RequestContext) {
			userID := c.Query("user_id")
			if userID == "" {
				c.JSON(consts.StatusBadRequest, map[string]string{"error": "user_id 是必需的"})
				return
			}

			isTemp := 1 // 默认临时会话（兼容未认证企业）
			if tmpStr := c.Query("is_temp"); tmpStr == "0" {
				isTemp = 0
			}

			// 选 client：query ?corp_id= 优先；不传则 router 第一个
			corpIDHint := c.Query("corp_id")
			var wecomClient *wecom.Client
			if corpIDHint != "" {
				if sc, ok := router.Lookup(corpIDHint); ok {
					wecomClient = sc.Client
				}
			}
			if wecomClient == nil {
				all := router.AllShops()
				if len(all) > 0 {
					wecomClient = all[0].Client
				}
			}
			if wecomClient == nil {
				c.JSON(consts.StatusServiceUnavailable, map[string]string{"error": "router 未加载任何店铺"})
				return
			}

			result, err := wecomClient.AddContactWay(ctx, userID, "chatwitheino", isTemp)
				if err != nil {
					log.Printf("[wecom] 创建联系我二维码失败: %v", err)
					c.JSON(consts.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("创建失败: %v", err)})
					return
				}

				// scene=2 时 API 会直接返回 qr_code；若未返回则通过 get_contact_way 补查
				if result.QrCode == "" && result.ConfigID != "" {
					log.Printf("[wecom] 未返回 qr_code，通过 config_id 补查: %s", result.ConfigID)
					detail, err := wecomClient.GetContactWay(ctx, result.ConfigID)
					if err != nil {
						log.Printf("[wecom] 查询二维码详情失败: %v", err)
					} else if detail.QrCode != "" {
						result.QrCode = detail.QrCode
					}
				}

				log.Printf("[wecom] 联系我二维码已生成: userID=%s configID=%s qrCode=%s", userID, result.ConfigID, result.QrCode)
				c.JSON(consts.StatusOK, map[string]interface{}{
					"qr_code":   result.QrCode,
					"config_id": result.ConfigID,
					"user_id":   userID,
					"tip":       "将此二维码展示给客户扫码，客户发送的消息将通过回调送达服务器",
				})
		})

		// 本地测试接口：模拟企业微信发送消息（多店版）
//
// 从 router 里取第一个店铺的 client 处理；指定 ?shop_id= 可覆盖。
		h.POST("/wecom/test/send", func(ctx context.Context, c *app.RequestContext) {
		var req struct {
			UserID  string `json:"user_id"`
			Content string `json:"content"`
			ShopID  string `json:"shop_id"`
		}

		if err := json.NewDecoder(bytes.NewReader(c.Request.Body())).Decode(&req); err != nil {
			c.JSON(consts.StatusBadRequest, map[string]string{"error": "无效的请求"})
			return
		}

		if req.UserID == "" || req.Content == "" {
			c.JSON(consts.StatusBadRequest, map[string]string{"error": "user_id 和 content 是必需的"})
			return
		}

		// 选店铺（req.ShopID 优先；否则 router 第一个）
		var shopID string
		var client *wecom.Client
		allShops := router.AllShops()
		if req.ShopID != "" {
			for _, sc := range allShops {
				if sc.ShopID == req.ShopID {
					shopID = sc.ShopID
					client = sc.Client
					break
				}
			}
		}
		if client == nil && len(allShops) > 0 {
			shopID = allShops[0].ShopID
			client = allShops[0].Client
		}
		if client == nil {
			c.JSON(consts.StatusServiceUnavailable, map[string]string{"error": "router 里没有可用店铺"})
			return
		}

		msg := &wecom.MessageXML{
			FromUserName: req.UserID,
			MsgType:      "text",
			Content:      req.Content,
			CreateTime:   time.Now().Unix(),
		}

		go s.handleWeComMessage(ctx, client, msg, shopID, "", "", c)

		c.JSON(consts.StatusOK, map[string]string{
			"message": "消息已发送，Agent 处理中",
			"user_id": req.UserID,
			"content": req.Content,
			"shop_id": shopID,
		})
	})
}

// handleWeComMessage 处理企业微信消息（多店版）
//
// 流程：构造带 shopID 的 ctx → 调 processAgentMessage → 用对应店铺的 client 回复
func (s *Server[M]) handleWeComMessage(ctx context.Context, client *wecom.Client, msg *wecom.MessageXML, shopID, timestamp, nonce string, c *app.RequestContext) {
	s.handleWeComMessageWithOpenKfID(ctx, client, msg, msg.OpenKfId, shopID)
}

// handleWeComMessageWithOpenKfID 处理企业微信消息，带openKfID + shopID（多店版）
func (s *Server[M]) handleWeComMessageWithOpenKfID(ctx context.Context, client *wecom.Client, msg *wecom.MessageXML, openKfID, shopID string) {
	sessionID := "wecom_" + shopID + "_" + msg.FromUserName // 加 shopID 防止多店用户串号
	sess, err := s.cfg.Store.GetOrCreate(sessionID)
	if err != nil {
		log.Printf("[wecom] 获取会话失败: %v", err)
		return
	}

	log.Printf("[wecom] 处理消息: session=%s shop=%s msg=%s history=%d", sessionID, shopID, msg.Content, len(sess.GetMessages()))

	// 注入 shopID 到 ctx，让 Agent 工具（create_appointment 等）能拿到
	ctxWithShop := tools.WithShopID(ctx, shopID)
	// v4.8: 透传微信 openID，让 create_appointment 自动建顾客档案（修 admin 顾客列表空 bug）
	// v4.9.3: KF 来源的 external_userid == FromUserName（同一字段）
	ctxWithUser := tools.WithOpenID(ctxWithShop, msg.FromUserName)
	ctxWithExt := tools.WithExternalUserID(ctxWithUser, msg.FromUserName)
	reply := s.processAgentMessage(ctxWithExt, sess, msg.Content, shopID)
	log.Printf("[wecom] Agent回复: %s", reply)

	// 发送回复消息
	if openKfID != "" {
		if err := client.SendKfTextMessage(ctx, msg.FromUserName, openKfID, reply); err != nil {
			log.Printf("[wecom] 客服消息发送失败: %v, 尝试普通消息", err)
			if err2 := client.SendTextMessage(ctx, msg.FromUserName, reply); err2 != nil {
				log.Printf("[wecom] 普通消息也失败: %v", err2)
			}
		} else {
			log.Printf("[wecom] 客服回复成功: to=%s openKfID=%s shop=%s", msg.FromUserName, openKfID, shopID)
		}
	} else {
		if err := client.SendTextMessage(ctx, msg.FromUserName, reply); err != nil {
			log.Printf("[wecom] 发送消息失败: %v", err)
		} else {
			log.Printf("[wecom] 发送回复成功: to=%s shop=%s", msg.FromUserName, shopID)
		}
	}
}

// buildTodayContext 返回动态日期上下文（用作 Agent Run 的第一条 user message，
// 让模型知道"今天/明天"对应到具体日期）。
func buildTodayContext() string {
	now := time.Now()
	today := now.Format("2006-01-02")
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	weekday := now.Weekday().String()
	return fmt.Sprintf("[系统上下文]\n当前日期：%s（%s）\n明天日期：%s\n用户说\"今天/明天/后天\"时请按上面的日期计算。\n",
		today, weekday, tomorrow)
}

// processAgentMessage 统一消息处理适配层
//
// 关键修复：以前这里直接调 s.cfg.ChatModel.Stream，绕过了 agent.go 里定义的 BarberAssistant，
// 导致 tools/create_appointment.go 等工具永远不会被调用、storage 里也写不进数据。
// 现在改为调 s.cfg.Agent.Run(ctx, input)，让 Agent 真正用上 tool calling。
//
// 注意：DeepAgent 每次 Run 不带历史，调用方要把"上下文 + 历史 + 当前用户消息"全部装进 input。
//
// shopID：注入 ctx，Agent 工具（create_appointment 等）通过 tools.ShopIDFromCtx 取。
//         也作为 system prompt 的提示，让 Agent 知道当前是哪家店。
// agentHistoryLimit 历史消息条数上限（env AGENT_HISTORY_LIMIT，默认 6，比之前 10 紧）
//
// 设计权衡：
//   - 条数太少（≤3）：顾客切话题后 agent 失忆
//   - 条数太多（≥12）：长预约工具调用（含 create_appointment / query_schedule）展开后 prompt 爆 token
//   - 默认 6 ≈ 3 轮对话，足够覆盖"查询→确认→执行"的主流链路
const defaultAgentHistoryLimit = 6

// agentHistoryMaxChars 历史消息字符预算（env AGENT_HISTORY_MAX_CHARS，默认 12000）
//
// 兜底：即使条数 ≤ limit，某条 tool_call 返回特别长也会爆；按字符再砍一次
const defaultAgentHistoryMaxChars = 12000

// trimHistory 按"条数上限 + 字符预算"双约束截断历史，从最新往旧取
//
// 行为：
//   - 先按 maxMessages 砍（取最后 N 条）
//   - 再按 maxChars 砍（从最新往旧累加，超出预算就停）
//   - 保证 assistant 完整：永远从 user/assistant 配对边界切，不会切到半条
func trimHistory[M adk.MessageType](history []M, maxMessages, maxChars int) []M {
	if len(history) == 0 {
		return history
	}

	// 1) 条数截断
	start := 0
	if len(history) > maxMessages {
		start = len(history) - maxMessages
	}
	out := append([]M(nil), history[start:]...)

	// 2) 字符预算截断（仅在超限时砍）
	//    累加顺序：从最新往旧；第 i 条加上后会超预算 → 砍掉 [0, i]（保留 i+1 起）
	totalChars := 0
	trimmed := false
	for i := len(out) - 1; i >= 0; i-- {
		n := msgLen(out[i])
		if totalChars+n > maxChars {
			out = out[i+1:] // 从 i+1 开始保留
			trimmed = true
			break          // 后续更早的不用算了
		}
		totalChars += n
	}
	_ = trimmed // 防止 unused；后续如果要加日志可读
	return out
}

// msgLen 估算一条消息的字符数（文本 + tool call 名 + 工具返回）
//
// 不精确（不模拟 tokenizer），但字符数是合理的粗粒度近似：
// 中文 1 字符 ≈ 1 token，英文 4 字符 ≈ 1 token，预算 12k 字符 ≈ 3k-6k token
func msgLen[M adk.MessageType](m M) int {
	switch mm := any(m).(type) {
	case *schema.Message:
		if mm == nil {
			return 0
		}
		s := len(mm.Content) + len(mm.ReasoningContent) + len(mm.ToolName)
		for _, tc := range mm.ToolCalls {
			s += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
		return s
	case *schema.AgenticMessage:
		if mm == nil {
			return 0
		}
		s := 0
		for _, b := range mm.ContentBlocks {
			if b == nil {
				continue
			}
			if b.Reasoning != nil {
				s += len(b.Reasoning.Text)
			}
			if b.UserInputText != nil {
				s += len(b.UserInputText.Text)
			}
			if b.AssistantGenText != nil {
				s += len(b.AssistantGenText.Text)
			}
			if b.FunctionToolCall != nil {
				s += len(b.FunctionToolCall.Name) + len(b.FunctionToolCall.Arguments)
			}
			if b.FunctionToolResult != nil {
				for _, c := range b.FunctionToolResult.Content {
					if c != nil && c.Text != nil {
						s += len(c.Text.Text)
					}
				}
			}
		}
		return s
	}
	return 0
}

// getEnvInt 读 env 整数（解析失败用兜底）
func getEnvInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func (s *Server[M]) processAgentMessage(ctx context.Context, sess *mem.Session[M], userContent, shopID string) string {
	history := sess.GetMessages()

	// 历史消息精简：先按条数砍，再按字符预算砍（v4.9.2）
	//   AGENT_HISTORY_LIMIT     默认 6     （之前是 10，太宽）
	//   AGENT_HISTORY_MAX_CHARS 默认 12000 （粗粒度 token 上限，≈ 3-6k token）
	maxMessages := getEnvInt("AGENT_HISTORY_LIMIT", defaultAgentHistoryLimit)
	maxChars := getEnvInt("AGENT_HISTORY_MAX_CHARS", defaultAgentHistoryMaxChars)
	history = trimHistory(history, maxMessages, maxChars)

	// 构造消息列表：第一条 user 是动态日期上下文 + 店铺上下文
	ctxMsg := buildTodayContext()
	if shopID != "" {
		ctxMsg += fmt.Sprintf("\n[店铺上下文]\n当前服务店铺 ID：%s\n（创建预约时工具会自动用这个店铺，无需在工具参数里指定）\n", shopID)
	}
	var messages []M
	messages = append(messages, msgops.NewUser[M](ctxMsg))

	// 历史消息（已精简）
	messages = append(messages, history...)

	// 当前用户消息
	messages = append(messages, msgops.NewUser[M](userContent))

	// 调 Agent Run（这是修复的关键！以前是 ChatModel.Stream）
	input := &adk.TypedAgentInput[M]{
		Messages:        messages,
		EnableStreaming: false,
	}
	events := s.cfg.Agent.Run(ctx, input)

	// Drain events，提取最后的 assistant 文本回复
	reply := s.drainAgentEvents(events)
	if reply == "" {
		reply = "抱歉，我暂时无法处理您的请求，请稍后再试。"
	}

	// 保存消息到会话
	sess.Append(msgops.NewUser[M](userContent))
	sess.Append(msgops.NewAssistant[M](reply, nil))

	return reply
}

// drainAgentEvents 把 Agent.Run 的事件流整理成最终的 assistant 回复文本。
//
// DeepAgent 的事件里既有最终 assistant 消息（Output.MessageOutput 非空），也有中间过程；
// 我们取最后一个含非空文本的 assistant 消息作为回复。
func (s *Server[M]) drainAgentEvents(events *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) string {
	var lastText string
	if events == nil {
		return ""
	}
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		text := msgops.AssistantText(msg)
		if text != "" {
			lastText = text
		}
	}
	return strings.TrimSpace(lastText)
}

// handleWeComMessageWithOpenKfID 旧版已迁移到带 shopID 版本（见上）

// kfMessageTracker 追踪微信客服消息的状态（分页游标 + msgid去重缓存）
type kfMessageTracker struct {
	mu          sync.Mutex
	cursor      string               // 上一次 sync_msg 返回的 next_cursor
	seenMsgIDs  map[string]time.Time // 已处理的msgid → 处理时间，保留最近100条
	lastCleanup time.Time
}

// getKfTracker 获取或创建客服消息追踪器
func (s *Server[M]) getKfTracker(openKfID string) *kfMessageTracker {
	val, _ := s.kfTracker.LoadOrStore(openKfID, &kfMessageTracker{
		seenMsgIDs: make(map[string]time.Time),
	})
	return val.(*kfMessageTracker)
}

// markSeen 标记消息已处理，返回false表示已存在（重复）
func (t *kfMessageTracker) markSeen(msgID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 清理过期记录（1小时）
	now := time.Now()
	if now.Sub(t.lastCleanup) > 5*time.Minute {
		for id, ts := range t.seenMsgIDs {
			if now.Sub(ts) > time.Hour {
				delete(t.seenMsgIDs, id)
			}
		}
		t.lastCleanup = now
	}

	if _, exists := t.seenMsgIDs[msgID]; exists {
		return false
	}

	// 保留最近100条
	if len(t.seenMsgIDs) >= 100 {
		var oldestID string
		var oldestTime time.Time
		for id, ts := range t.seenMsgIDs {
			if oldestID == "" || ts.Before(oldestTime) {
				oldestID = id
				oldestTime = ts
			}
		}
		delete(t.seenMsgIDs, oldestID)
	}

	t.seenMsgIDs[msgID] = now
	return true
}

// updateCursor 更新同步游标
func (t *kfMessageTracker) updateCursor(newCursor string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cursor = newCursor
}

// getCursor 获取当前游标
func (t *kfMessageTracker) getCursor() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cursor
}

// handleKfCallback 处理微信客服回调事件
func (s *Server[M]) handleKfCallback(ctx context.Context, client *wecom.Client, callback *wecom.MessageXML, shopID string) {
	openKfID := callback.OpenKfId
	log.Printf("[kf] 收到客服事件: shop=%s token=%s openKfId=%s", shopID, callback.Token, openKfID)

	tracker := s.getKfTracker(openKfID)

	// 使用 cursor 只拉取增量消息
	cursor := tracker.getCursor()
	log.Printf("[kf] 使用cursor拉取: cursor=%q (首次=%v)", cursor, cursor == "")

	result, err := client.SyncMsg(ctx, cursor, callback.Token, 50)
	if err != nil {
		log.Printf("[kf] 拉取消息失败: %v", err)
		return
	}

	// 更新cursor
	if result.NextCursor != "" {
		tracker.updateCursor(result.NextCursor)
	}

	log.Printf("[kf] 拉取到 %d 条消息, has_more=%d next_cursor=%s",
		len(result.MsgList), result.HasMore, result.NextCursor)

	// 过滤客户文本消息
	var textMsgs []wecom.KfMsgItem
	for _, kfMsg := range result.MsgList {
		if kfMsg.Origin == 3 && kfMsg.MsgType == "text" && kfMsg.Text != nil {
			textMsgs = append(textMsgs, kfMsg)
		}
	}

	// 关键优化：如果cursor为空(首次拉取)且有多条消息，只处理最后一条
	// 历史积压消息直接跳过，后续用cursor只拉增量就不会有问题
	if cursor == "" && len(textMsgs) > 1 {
		log.Printf("[kf] 首次拉取，跳过前%d条历史消息，只处理最后1条",
			len(textMsgs)-1)
		textMsgs = textMsgs[len(textMsgs)-1:]
	}

	for _, kfMsg := range textMsgs {
		// msgid去重
		if !tracker.markSeen(kfMsg.Msgid) {
			log.Printf("[kf] 重复消息跳过: msgid=%s", kfMsg.Msgid)
			continue
		}

		log.Printf("[kf] 处理: user=%s msg=%s msgid=%s",
			kfMsg.ExternalUserid, kfMsg.Text.Content, kfMsg.Msgid)

		msg := &wecom.MessageXML{
			FromUserName: kfMsg.ExternalUserid,
			OpenKfId:     kfMsg.OpenKfid,
			MsgType:      "text",
			Content:      kfMsg.Text.Content,
		}
		go s.handleWeComMessageWithOpenKfID(ctx, client, msg, kfMsg.OpenKfid, shopID)
	}

	log.Printf("[kf] 完成: 总%d条 文本%d条 处理%d条", len(result.MsgList), len(textMsgs), len(textMsgs))
}

// handleAddExternalContact 处理外部联系人添加事件（add_external_contact）
// 当客户通过"联系我"二维码添加理发师为好友时，企业微信会推送此事件。
//
// 事件字段说明：
//   - ExternalUserID: 新添加的外部联系人 UserID
//   - FromUserName (UserID): 执行添加操作的企业成员 UserID
//   - WelcomeCode: 欢迎码，可用于调用 send_welcome_msg 发送欢迎语
//
// 注意事项：
//   - WelcomeCode 有时效性（通常 20 秒内有效），需要尽快使用
//   - 若 API 返回 45078 错误码，说明企业微信已自动下发了欢迎语（配置了自动回复规则），此时无需重复发送
//   - 外部联系人模式需要在企业微信管理后台配置"联系我"二维码和应用可调用权限
func (s *Server[M]) handleAddExternalContact(ctx context.Context, client *wecom.Client, msg *wecom.MessageXML, shopID string) {
	externalUserID := msg.ExternalUserID
	if externalUserID == "" {
		externalUserID = msg.FromUserName
	}
	employeeUserID := msg.UserID

	log.Printf("[external] 收到添加外部联系人事件: shop=%s ExternalUserID=%s EmployeeUserID=%s WelcomeCode=%s",
		shopID, externalUserID, employeeUserID, msg.WelcomeCode)

	// 从 Shop 表查 KFLink（多店版不再用 WeComConfig.KFLink）
	var kfLink string
	if shop, _ := storage.GetShopByID(ctx, shopID); shop != nil {
		kfLink = shop.WecomKFLink
	}
	if kfLink == "" && s.cfg.WeComConfig != nil {
		kfLink = s.cfg.WeComConfig.KFLink // 兼容旧 fallback
	}

	// 给客户发欢迎语（仅当有 WelcomeCode 时）
	if msg.WelcomeCode != "" {
		welcomeText := buildWelcomeText(kfLink)
		if err := client.SendWelcomeMsg(ctx, msg.WelcomeCode, welcomeText); err != nil {
			log.Printf("[external] 欢迎语发送失败(WelcomeCode方式): %v", err)
		} else {
			log.Printf("[external] 欢迎语发送成功(WelcomeCode): ExternalUserID=%s", externalUserID)
		}
	} else {
		log.Printf("[external] 无 WelcomeCode，无法给客户发送欢迎语")
	}

	// 同时通知员工：有新客户添加，引导客户进入KF
	if employeeUserID != "" && kfLink != "" {
		notifyText := fmt.Sprintf(
			"🔔 有新客户添加了你（ExternalUserID: %s）\n\n请将以下客服链接发给客户，后续对话将由AI助手处理：\n%s",
			externalUserID, kfLink,
		)
		if err := client.SendTextMessage(ctx, employeeUserID, notifyText); err != nil {
			log.Printf("[external] 通知员工失败: %v", err)
		} else {
			log.Printf("[external] 已通知员工 %s，新客户: %s", employeeUserID, externalUserID)
		}
	}
}

// buildWelcomeText 构建客户欢迎语
func buildWelcomeText(kfLink string) string {
	text := "您好！欢迎添加我们的理发预约助手。"
	if kfLink != "" {
		text += fmt.Sprintf("请点击链接进入客服对话，我可以帮您查询排班、创建预约和取消预约：\n%s", kfLink)
	} else {
		text += "请稍候，我们的客服将尽快为您服务。"
	}
	return text
}

// handleExternalContactMessage 处理外部联系人发送的单聊文本消息
// 通过统一消息适配层（UserMessage）处理消息，复用 Agent 核心逻辑（processAgentMessage）。
//
// 外部联系人消息与微信客服消息的区别：
//   - 微信客服通过 SyncMsg 接口拉取消息，使用 KfTextMessage 回复
//   - 外部联系人消息由企业微信直接推送到回调 URL，使用 externalcontact/send_msg 回复
//   - 外部联系人回复的 sender 必须是已配置可调用应用的企业成员
//
// 外部联系人文本消息回调 XML 字段说明：
//   - FromUserName: 外部联系人的 ExternalUserID
//   - ToUserName:   接收消息的企业成员 UserID
//   - Content:      消息文本内容
func (s *Server[M]) handleExternalContactMessage(ctx context.Context, client *wecom.Client, msg *wecom.MessageXML, shopID string) {
	externalUserID := msg.ExternalUserID
	if externalUserID == "" {
		externalUserID = msg.FromUserName
	}
	employeeUserID := msg.ToUserName

	log.Printf("[external] 收到外部联系人消息: shop=%s ExternalUserID=%s EmployeeUserID=%s Content=%s",
		shopID, externalUserID, employeeUserID, msg.Content)

	userMsg := wecom.FromExternalContactMsg(externalUserID, employeeUserID, msg.Content)

	sessionID := "external_" + shopID + "_" + externalUserID // 加 shopID 防止多店用户串号
	sess, err := s.cfg.Store.GetOrCreate(sessionID)
	if err != nil {
		log.Printf("[external] 获取会话失败: %v", err)
		return
	}

	log.Printf("[external] 处理消息: session=%s msg=%s history=%d", sessionID, userMsg.Content, len(sess.GetMessages()))

	ctxWithShop := tools.WithShopID(ctx, shopID)
	// v4.9.3 修复：之前这条路径完全没透传任何 wecom ID，
	//   导致外部联系人预约建的顾客档案 openID/external_user_id 都是空 → cron 全失败
	//   - openID: 在外部联系人场景下用 employee userid（不完美但至少能定位）
	//   - external_user_id: 真实 external ID（reminder 优先用这个）
	ctxWithUser := tools.WithOpenID(ctxWithShop, employeeUserID)
	ctxWithExt := tools.WithExternalUserID(ctxWithUser, externalUserID)
	reply := s.processAgentMessage(ctxWithExt, sess, userMsg.Content, shopID)
	log.Printf("[external] Agent回复: %s", reply)

	// 使用统一回复适配层发送回复
	// 外部联系人场景：调用 /cgi-bin/externalcontact/send_msg 接口
	replyReq := &wecom.ReplyRequest{
		UserID:         userMsg.UserID,
		ExternalUserID: externalUserID,
		Content:        reply,
		SourceType:     wecom.SourceExternal,
		EmployeeUserID: userMsg.EmployeeUserID,
	}

	if err := client.SendReply(ctx, replyReq); err != nil {
		log.Printf("[external] 发送外部联系人回复失败: %v", err)
	} else {
		log.Printf("[external] 回复成功: ExternalUserID=%s EmployeeUserID=%s", externalUserID, userMsg.EmployeeUserID)
	}
}
