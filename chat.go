package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// supportSellerID is the sentinel SellerID value that marks a conversation
// as a general customer-support thread (routed to any admin) rather than a
// buyer-seller chat about a specific listing.
const supportSellerID = "support"

// findOrCreateConversation returns the existing thread between buyer and
// sellerID if one exists, or creates a new one. One thread per
// buyer/seller pair (not per listing) keeps things simple - ListingID is
// just "what this conversation was originally about" for context.
func (a *App) findOrCreateConversation(buyerID, sellerID, listingID string) *Conversation {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	for _, c := range a.store.Conversations {
		if c.BuyerID == buyerID && c.SellerID == sellerID {
			return c
		}
	}
	c := &Conversation{
		ID:        newID(),
		BuyerID:   buyerID,
		SellerID:  sellerID,
		ListingID: listingID,
		CreatedAt: time.Now(),
	}
	a.store.Conversations[c.ID] = c
	a.store.save()
	return c
}

// canAccessConversation reports whether user is allowed to view/post in
// this conversation: its buyer, its seller, or (for support threads) any
// admin.
func canAccessConversation(c *Conversation, user *User) bool {
	if user == nil {
		return false
	}
	if c.BuyerID == user.ID {
		return true
	}
	if c.SellerID == supportSellerID {
		return user.Role == "admin"
	}
	return c.SellerID == user.ID
}

// handleChatOpen starts (or resumes) a conversation with a specific seller,
// optionally about a specific listing, then redirects to view it. This is
// what the "Chat with seller" link on a product page hits.
func (a *App) handleChatOpen(w http.ResponseWriter, r *http.Request, user *User) {
	sellerID := r.URL.Query().Get("seller_id")
	listingID := r.URL.Query().Get("listing_id")
	if sellerID == "" || sellerID == user.ID {
		http.Redirect(w, r, "/inbox", http.StatusSeeOther)
		return
	}
	c := a.findOrCreateConversation(user.ID, sellerID, listingID)
	http.Redirect(w, r, "/chat/view?id="+c.ID, http.StatusSeeOther)
}

// handleSupportOpen starts (or resumes) the current user's general
// customer-support thread.
func (a *App) handleSupportOpen(w http.ResponseWriter, r *http.Request, user *User) {
	c := a.findOrCreateConversation(user.ID, supportSellerID, "")
	http.Redirect(w, r, "/chat/view?id="+c.ID, http.StatusSeeOther)
}

type chatMessageView struct {
	Message    *Message
	FromMe     bool
	SenderName string
}

func (a *App) handleChatView(w http.ResponseWriter, r *http.Request, user *User) {
	id := r.URL.Query().Get("id")
	a.store.mu.RLock()
	conv, ok := a.store.Conversations[id]
	a.store.mu.RUnlock()

	if !ok || !canAccessConversation(conv, user) {
		http.Redirect(w, r, "/inbox", http.StatusSeeOther)
		return
	}

	a.store.mu.RLock()
	msgs := make([]*Message, 0)
	for _, m := range a.store.Messages {
		if m.ConversationID == id {
			msgs = append(msgs, m)
		}
	}
	var otherName string
	var listing *Listing
	if conv.SellerID == supportSellerID {
		if user.ID == conv.BuyerID {
			otherName = "Kaya Support"
		} else if b, ok := a.store.Users[conv.BuyerID]; ok {
			otherName = b.Name // admin viewing - show who they're helping
		}
	} else if conv.BuyerID == user.ID {
		if s, ok := a.store.Users[conv.SellerID]; ok {
			otherName = s.Name
		}
	} else {
		if b, ok := a.store.Users[conv.BuyerID]; ok {
			otherName = b.Name
		}
	}
	if conv.ListingID != "" {
		listing = a.store.Listings[conv.ListingID]
	}
	a.store.mu.RUnlock()

	sort.Slice(msgs, func(i, j int) bool { return msgs[i].CreatedAt.Before(msgs[j].CreatedAt) })

	views := make([]chatMessageView, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, chatMessageView{Message: m, FromMe: m.SenderID == user.ID})
	}

	render(w, "chat_view.html", map[string]any{
		"Conversation": conv,
		"Messages":     views,
		"OtherName":    otherName,
		"Listing":      listing,
		"User":         user,
		"IsSupport":    conv.SellerID == supportSellerID,
	})
}

func (a *App) handleChatSend(w http.ResponseWriter, r *http.Request, user *User) {
	id := r.FormValue("conversation_id")
	body := strings.TrimSpace(r.FormValue("body"))

	a.store.mu.Lock()
	conv, ok := a.store.Conversations[id]
	if !ok || !canAccessConversation(conv, user) || body == "" {
		a.store.mu.Unlock()
		http.Redirect(w, r, "/inbox", http.StatusSeeOther)
		return
	}
	msg := &Message{
		ID:             newID(),
		ConversationID: id,
		SenderID:       user.ID,
		Body:           body,
		CreatedAt:      time.Now(),
	}
	a.store.Messages[msg.ID] = msg
	a.store.save()
	a.store.mu.Unlock()

	http.Redirect(w, r, "/chat/view?id="+id, http.StatusSeeOther)
}

type conversationSummary struct {
	Conversation *Conversation
	OtherName    string
	LastMessage  string
	LastAt       time.Time
	IsSupport    bool
}

// handleInbox lists every conversation the current user is part of, either
// as the buyer or as the seller being messaged.
func (a *App) handleInbox(w http.ResponseWriter, r *http.Request, user *User) {
	a.store.mu.RLock()
	summaries := make([]conversationSummary, 0)
	for _, c := range a.store.Conversations {
		if c.BuyerID != user.ID && c.SellerID != user.ID {
			continue
		}
		var otherName string
		if c.SellerID == supportSellerID {
			otherName = "Kaya Support"
		} else if c.BuyerID == user.ID {
			if s, ok := a.store.Users[c.SellerID]; ok {
				otherName = s.Name
			}
		} else {
			if b, ok := a.store.Users[c.BuyerID]; ok {
				otherName = b.Name
			}
		}

		var lastMsg *Message
		for _, m := range a.store.Messages {
			if m.ConversationID != c.ID {
				continue
			}
			if lastMsg == nil || m.CreatedAt.After(lastMsg.CreatedAt) {
				lastMsg = m
			}
		}
		sum := conversationSummary{Conversation: c, OtherName: otherName, IsSupport: c.SellerID == supportSellerID, LastAt: c.CreatedAt}
		if lastMsg != nil {
			sum.LastMessage = lastMsg.Body
			sum.LastAt = lastMsg.CreatedAt
		}
		summaries = append(summaries, sum)
	}
	a.store.mu.RUnlock()

	sort.Slice(summaries, func(i, j int) bool { return summaries[i].LastAt.After(summaries[j].LastAt) })

	render(w, "inbox.html", map[string]any{
		"Conversations": summaries,
		"User":          user,
	})
}

// handleAdminSupport lists every general support conversation for admins to
// respond to - the "general customer care center" view.
func (a *App) handleAdminSupport(w http.ResponseWriter, r *http.Request, admin *User) {
	a.store.mu.RLock()
	summaries := make([]conversationSummary, 0)
	for _, c := range a.store.Conversations {
		if c.SellerID != supportSellerID {
			continue
		}
		var otherName string
		if b, ok := a.store.Users[c.BuyerID]; ok {
			otherName = b.Name
		}
		var lastMsg *Message
		for _, m := range a.store.Messages {
			if m.ConversationID != c.ID {
				continue
			}
			if lastMsg == nil || m.CreatedAt.After(lastMsg.CreatedAt) {
				lastMsg = m
			}
		}
		sum := conversationSummary{Conversation: c, OtherName: otherName, IsSupport: true, LastAt: c.CreatedAt}
		if lastMsg != nil {
			sum.LastMessage = lastMsg.Body
			sum.LastAt = lastMsg.CreatedAt
		}
		summaries = append(summaries, sum)
	}
	a.store.mu.RUnlock()

	sort.Slice(summaries, func(i, j int) bool { return summaries[i].LastAt.After(summaries[j].LastAt) })

	render(w, "admin_support.html", map[string]any{
		"Conversations": summaries,
		"User":          admin,
	})
}
