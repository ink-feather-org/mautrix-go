// Copyright (c) 2024 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/exgjson"
	"golang.org/x/net/context"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	getEventBaseQuery = `
		SELECT rowid, -1, room_id, event_id, sender, type, state_key, timestamp, content, decrypted, decrypted_type, unsigned,
		       redacted_by, relates_to, relation_type, megolm_session_id, decryption_error, reactions, last_edit_rowid
		FROM event
	`
	getEventByID                     = getEventBaseQuery + `WHERE event_id = $1`
	getFailedEventsByMegolmSessionID = getEventBaseQuery + `WHERE room_id = $1 AND megolm_session_id = $2 AND decryption_error IS NOT NULL`
	upsertEventQuery                 = `
		INSERT INTO event (room_id, event_id, sender, type, state_key, timestamp, content, decrypted, decrypted_type, unsigned, redacted_by, relates_to, relation_type, megolm_session_id, decryption_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (event_id) DO UPDATE
			SET decrypted=COALESCE(event.decrypted, excluded.decrypted),
			    decrypted_type=COALESCE(event.decrypted_type, excluded.decrypted_type),
			    redacted_by=COALESCE(event.redacted_by, excluded.redacted_by),
			    decryption_error=CASE WHEN COALESCE(event.decrypted, excluded.decrypted) IS NULL THEN COALESCE(excluded.decryption_error, event.decryption_error) END
		RETURNING rowid
	`
	updateEventDecryptedQuery = `UPDATE event SET decrypted = $1, decrypted_type = $2, decryption_error = NULL WHERE rowid = $3`
	getEventReactionsQuery    = getEventBaseQuery + `
		WHERE room_id = ?
		  AND type = 'm.reaction'
		  AND relation_type = 'm.annotation'
		  AND redacted_by IS NULL
		  AND relates_to IN (%s)
	`
	getEventEditRowIDsQuery = `
		SELECT main.event_id, edit.rowid
		FROM event main
		JOIN event edit ON
			edit.room_id = main.room_id
			AND edit.relates_to = main.event_id
			AND edit.relation_type = 'm.replace'
		AND edit.type = main.type
		AND edit.sender = main.sender
		AND edit.redacted_by IS NULL
		WHERE main.event_id IN (%s)
		ORDER BY main.event_id, edit.timestamp
	`
	setLastEditRowIDQuery = `
		UPDATE event SET last_edit_rowid = $2 WHERE event_id = $1
	`
	updateReactionCountsQuery = `UPDATE event SET reactions = $2 WHERE event_id = $1`
)

type EventQuery struct {
	*dbutil.QueryHelper[*Event]
}

func (eq *EventQuery) GetFailedByMegolmSessionID(ctx context.Context, roomID id.RoomID, sessionID id.SessionID) ([]*Event, error) {
	return eq.QueryMany(ctx, getFailedEventsByMegolmSessionID, roomID, sessionID)
}

func (eq *EventQuery) GetByID(ctx context.Context, eventID id.EventID) (*Event, error) {
	return eq.QueryOne(ctx, getEventByID, eventID)
}

func (eq *EventQuery) Upsert(ctx context.Context, evt *Event) (rowID EventRowID, err error) {
	err = eq.GetDB().QueryRow(ctx, upsertEventQuery, evt.sqlVariables()...).Scan(&rowID)
	if err == nil {
		evt.RowID = rowID
	}
	return
}

func (eq *EventQuery) UpdateDecrypted(ctx context.Context, rowID EventRowID, decrypted json.RawMessage, decryptedType string) error {
	return eq.Exec(ctx, updateEventDecryptedQuery, unsafeJSONString(decrypted), decryptedType, rowID)
}

func (eq *EventQuery) FillReactionCounts(ctx context.Context, roomID id.RoomID, events []*Event) error {
	eventIDs := make([]id.EventID, 0)
	eventMap := make(map[id.EventID]*Event)
	for i, evt := range events {
		if evt.Reactions == nil {
			eventIDs[i] = evt.ID
			eventMap[evt.ID] = evt
		}
	}
	result, err := eq.GetReactions(ctx, roomID, eventIDs...)
	if err != nil {
		return err
	}
	for evtID, res := range result {
		eventMap[evtID].Reactions = res.Counts
	}
	return nil
}

func (eq *EventQuery) FillLastEditRowIDs(ctx context.Context, roomID id.RoomID, events []*Event) error {
	eventIDs := make([]id.EventID, 0)
	eventMap := make(map[id.EventID]*Event)
	for i, evt := range events {
		if evt.LastEditRowID == 0 {
			eventIDs[i] = evt.ID
			eventMap[evt.ID] = evt
		}
	}
	return eq.GetDB().DoTxn(ctx, nil, func(ctx context.Context) error {
		result, err := eq.GetEditRowIDs(ctx, roomID, eventIDs...)
		if err != nil {
			return err
		}
		for evtID, res := range result {
			lastEditRowID := res[len(res)-1]
			eventMap[evtID].LastEditRowID = lastEditRowID
			err = eq.Exec(ctx, setLastEditRowIDQuery, evtID, lastEditRowID)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

var reactionKeyPath = exgjson.Path("m.relates_to", "key")

type GetReactionsResult struct {
	Events []*Event
	Counts map[string]int
}

func buildMultiEventGetFunction(roomID id.RoomID, eventIDs []id.EventID, query string) (string, []any) {
	params := make([]any, len(eventIDs)+1)
	params[0] = roomID
	for i, evtID := range eventIDs {
		params[i+1] = evtID
	}
	placeholders := strings.Repeat("?,", len(eventIDs))
	placeholders = placeholders[:len(placeholders)-1]
	return fmt.Sprintf(query, placeholders), params
}

type editRowIDTuple struct {
	eventID   id.EventID
	editRowID EventRowID
}

func (eq *EventQuery) GetEditRowIDs(ctx context.Context, roomID id.RoomID, eventIDs ...id.EventID) (map[id.EventID][]EventRowID, error) {
	query, params := buildMultiEventGetFunction(roomID, eventIDs, getEventEditRowIDsQuery)
	rows, err := eq.GetDB().Query(ctx, query, params...)
	output := make(map[id.EventID][]EventRowID)
	return output, dbutil.NewRowIterWithError(rows, func(row dbutil.Scannable) (tuple editRowIDTuple, err error) {
		err = row.Scan(&tuple.eventID, &tuple.editRowID)
		return
	}, err).Iter(func(tuple editRowIDTuple) (bool, error) {
		output[tuple.eventID] = append(output[tuple.eventID], tuple.editRowID)
		return true, nil
	})
}

func (eq *EventQuery) GetReactions(ctx context.Context, roomID id.RoomID, eventIDs ...id.EventID) (map[id.EventID]*GetReactionsResult, error) {
	result := make(map[id.EventID]*GetReactionsResult, len(eventIDs))
	for _, evtID := range eventIDs {
		result[evtID] = &GetReactionsResult{Counts: make(map[string]int)}
	}
	return result, eq.GetDB().DoTxn(ctx, nil, func(ctx context.Context) error {
		query, params := buildMultiEventGetFunction(roomID, eventIDs, getEventReactionsQuery)
		events, err := eq.QueryMany(ctx, query, params...)
		if err != nil {
			return err
		} else if len(events) == 0 {
			return nil
		}
		for _, evt := range events {
			dest := result[evt.RelatesTo]
			dest.Events = append(dest.Events, evt)
			keyRes := gjson.GetBytes(evt.Content, reactionKeyPath)
			if keyRes.Type == gjson.String {
				dest.Counts[keyRes.Str]++
			}
		}
		for evtID, res := range result {
			if len(res.Counts) > 0 {
				err = eq.Exec(ctx, updateReactionCountsQuery, evtID, dbutil.JSON{Data: &res.Counts})
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

type EventRowID int64

func (m EventRowID) GetMassInsertValues() [1]any {
	return [1]any{m}
}

type Event struct {
	RowID         EventRowID    `json:"fi.mau.hicli.rowid"`
	TimelineRowID TimelineRowID `json:"fi.mau.hicli.timeline_rowid"`

	RoomID    id.RoomID  `json:"room_id"`
	ID        id.EventID `json:"event_id"`
	Sender    id.UserID  `json:"sender"`
	Type      string     `json:"type"`
	StateKey  *string    `json:"state_key,omitempty"`
	Timestamp time.Time  `json:"timestamp"`

	Content       json.RawMessage `json:"content"`
	Decrypted     json.RawMessage `json:"decrypted,omitempty"`
	DecryptedType string          `json:"decrypted_type,omitempty"`
	Unsigned      json.RawMessage `json:"unsigned,omitempty"`

	RedactedBy   id.EventID         `json:"redacted_by,omitempty"`
	RelatesTo    id.EventID         `json:"relates_to,omitempty"`
	RelationType event.RelationType `json:"relation_type,omitempty"`

	MegolmSessionID id.SessionID `json:"-,omitempty"`
	DecryptionError string

	Reactions     map[string]int
	LastEditRowID EventRowID
}

func MautrixToEvent(evt *event.Event) *Event {
	dbEvt := &Event{
		RoomID:          evt.RoomID,
		ID:              evt.ID,
		Sender:          evt.Sender,
		Type:            evt.Type.Type,
		StateKey:        evt.StateKey,
		Timestamp:       time.UnixMilli(evt.Timestamp),
		Content:         evt.Content.VeryRaw,
		MegolmSessionID: getMegolmSessionID(evt),
	}
	dbEvt.RelatesTo, dbEvt.RelationType = getRelatesTo(evt)
	dbEvt.Unsigned, _ = json.Marshal(&evt.Unsigned)
	if evt.Unsigned.RedactedBecause != nil {
		dbEvt.RedactedBy = evt.Unsigned.RedactedBecause.ID
	}
	return dbEvt
}

func (e *Event) AsRawMautrix() *event.Event {
	evt := &event.Event{
		RoomID:    e.RoomID,
		ID:        e.ID,
		Sender:    e.Sender,
		Type:      event.Type{Type: e.Type, Class: event.MessageEventType},
		StateKey:  e.StateKey,
		Timestamp: e.Timestamp.UnixMilli(),
		Content:   event.Content{VeryRaw: e.Content},
	}
	if e.Decrypted != nil {
		evt.Content.VeryRaw = e.Decrypted
		evt.Type.Type = e.DecryptedType
		evt.Mautrix.WasEncrypted = true
	}
	if e.StateKey != nil {
		evt.Type.Class = event.StateEventType
	}
	_ = json.Unmarshal(e.Unsigned, &evt.Unsigned)
	return evt
}

func (e *Event) Scan(row dbutil.Scannable) (*Event, error) {
	var timestamp int64
	var redactedBy, relatesTo, relationType, megolmSessionID, decryptionError, decryptedType sql.NullString
	var lastEditRowID sql.NullInt64
	err := row.Scan(
		&e.RowID,
		&e.TimelineRowID,
		&e.RoomID,
		&e.ID,
		&e.Sender,
		&e.Type,
		&e.StateKey,
		&timestamp,
		(*[]byte)(&e.Content),
		(*[]byte)(&e.Decrypted),
		&decryptedType,
		(*[]byte)(&e.Unsigned),
		&redactedBy,
		&relatesTo,
		&relationType,
		&megolmSessionID,
		&decryptionError,
		dbutil.JSON{Data: &e.Reactions},
		&lastEditRowID,
	)
	if err != nil {
		return nil, err
	}
	e.Timestamp = time.UnixMilli(timestamp)
	e.RedactedBy = id.EventID(redactedBy.String)
	e.RelatesTo = id.EventID(relatesTo.String)
	e.RelationType = event.RelationType(relatesTo.String)
	e.MegolmSessionID = id.SessionID(megolmSessionID.String)
	e.DecryptedType = decryptedType.String
	e.DecryptionError = decryptionError.String
	e.LastEditRowID = EventRowID(lastEditRowID.Int64)
	return e, nil
}

var relatesToPath = exgjson.Path("m.relates_to", "event_id")
var relationTypePath = exgjson.Path("m.relates_to", "rel_type")

func getRelatesTo(evt *event.Event) (id.EventID, event.RelationType) {
	if evt.StateKey != nil {
		return "", ""
	}
	results := gjson.GetManyBytes(evt.Content.VeryRaw, relatesToPath, relationTypePath)
	if len(results) == 2 && results[0].Exists() && results[1].Exists() && results[0].Type == gjson.String && results[1].Type == gjson.String {
		return id.EventID(results[0].Str), event.RelationType(results[1].Str)
	}
	return "", ""
}

func getMegolmSessionID(evt *event.Event) id.SessionID {
	if evt.Type != event.EventEncrypted {
		return ""
	}
	res := gjson.GetBytes(evt.Content.VeryRaw, "session_id")
	if res.Exists() && res.Type == gjson.String {
		return id.SessionID(res.Str)
	}
	return ""
}

func (e *Event) sqlVariables() []any {
	var reactions any
	if e.Reactions != nil {
		reactions = e.Reactions
	}
	return []any{
		e.RoomID,
		e.ID,
		e.Sender,
		e.Type,
		e.StateKey,
		e.Timestamp.UnixMilli(),
		unsafeJSONString(e.Content),
		unsafeJSONString(e.Decrypted),
		dbutil.StrPtr(e.DecryptedType),
		unsafeJSONString(e.Unsigned),
		dbutil.StrPtr(e.RedactedBy),
		dbutil.StrPtr(e.RelatesTo),
		dbutil.StrPtr(e.RelationType),
		dbutil.StrPtr(e.MegolmSessionID),
		dbutil.StrPtr(e.DecryptionError),
		dbutil.JSON{Data: reactions},
		dbutil.NumPtr(e.LastEditRowID),
	}
}

func (e *Event) CanUseForPreview() bool {
	return (e.Type == event.EventMessage.Type || e.Type == event.EventSticker.Type ||
		(e.Type == event.EventEncrypted.Type &&
			(e.DecryptedType == event.EventMessage.Type || e.DecryptedType == event.EventSticker.Type))) &&
		e.RelationType != event.RelReplace
}

func (e *Event) BumpsSortingTimestamp() bool {
	return (e.Type == event.EventMessage.Type || e.Type == event.EventSticker.Type || e.Type == event.EventEncrypted.Type) &&
		e.RelationType != event.RelReplace
}
