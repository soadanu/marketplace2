package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// isUnread reports whether the newest message in a conversation is one the
// user hasn't seen yet: it was written by someone else, after the last time
// the user's side opened (or replied in) the conversation.
func isUnread(c *Conversation, last *Message, user *User) bool {
	if last == nil || last.SenderID == user.ID {
		return false
	}
	readAt := c.SellerReadAt // the seller, or support staff for support threads
	if c.BuyerID == user.ID {
		readAt = c.BuyerReadAt
	}
	return last.CreatedAt.After(readAt)
}

// markReadLocked records that user has seen the conversation up to t.
// Caller holds the store write lock.
func markReadLocked(c *Conversation, user *User, t time.Time) {
	if c.BuyerID == user.ID {
		c.BuyerReadAt = t
	} else {
		c.SellerReadAt = t
	}
}

// markRead marks a conversation as read now. persist=false keeps it in
// memory only, so the 4-second chat polling doesn't rewrite the whole data
// file every time.
func (a *App) markRead(c *Conversation, user *User, persist bool) {
	a.store.mu.Lock()
	markReadLocked(c, user, time.Now())
	if persist {
		a.store.save()
	}
	a.store.mu.Unlock()
}

// unreadCount is how many conversations have something new for the user.
func (a *App) unreadCount(user *User) int {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()

	last := map[string]*Message{}
	for _, m := range a.store.Messages {
		if cur, ok := last[m.ConversationID]; !ok || m.CreatedAt.After(cur.CreatedAt) {
			last[m.ConversationID] = m
		}
	}
	n := 0
	for _, c := range a.store.Conversations {
		mine := c.BuyerID == user.ID || c.SellerID == user.ID ||
			(c.SellerID == supportSellerID && user.Role == "admin")
		if mine && isUnread(c, last[c.ID], user) {
			n++
		}
	}
	return n
}

// handleCounts feeds the little badges in the menu (cart items, unread chats).
func (a *App) handleCounts(w http.ResponseWriter, r *http.Request) {
	cartN := 0
	for _, q := range readCart(r) {
		cartN += q
	}
	unread := 0
	if u := a.currentUser(r); u != nil {
		unread = a.unreadCount(u)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]int{"cart": cartN, "unread": unread})
}

type pollMessage struct {
	ID     string `json:"id"`
	FromMe bool   `json:"from_me"`
	Body   string `json:"body"`
	Time   string `json:"time"`
	TS     string `json:"ts"`
}

// handleChatPoll returns messages newer than ?after= so an open chat page
// can show new messages without a refresh.
func (a *App) handleChatPoll(w http.ResponseWriter, r *http.Request, user *User) {
	id := r.URL.Query().Get("id")
	after, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("after"))

	a.store.mu.RLock()
	conv, ok := a.store.Conversations[id]
	if !ok || !canAccessConversation(conv, user) {
		a.store.mu.RUnlock()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	msgs := make([]*Message, 0)
	for _, m := range a.store.Messages {
		if m.ConversationID == id && m.CreatedAt.After(after) {
			msgs = append(msgs, m)
		}
	}
	a.store.mu.RUnlock()
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].CreatedAt.Before(msgs[j].CreatedAt) })

	out := make([]pollMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, pollMessage{
			ID: m.ID, FromMe: m.SenderID == user.ID, Body: m.Body,
			Time: m.CreatedAt.Format("Jan 2, 3:04 PM"), TS: m.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	if len(out) > 0 {
		a.markRead(conv, user, false)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"messages": out})
}
