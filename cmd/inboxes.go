package main

import (
	"strconv"

	"github.com/abhinavxd/artemis/internal/envelope"
	imodels "github.com/abhinavxd/artemis/internal/inbox/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func handleGetInboxes(r *fastglue.Request) error {
	var app = r.Context.(*App)
	inboxes, err := app.inbox.GetAll()
	// TODO: Clear out passwords.
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Could not fetch inboxes", nil, envelope.GeneralError)
	}
	return r.SendEnvelope(inboxes)
}

func handleGetInbox(r *fastglue.Request) error {
	var (
		app   = r.Context.(*App)
		id, _ = strconv.Atoi(r.RequestCtx.UserValue("id").(string))
	)
	inbox, err := app.inbox.GetByID(id)
	// TODO: Clear out passwords.
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Could not fetch inboxes", nil, envelope.GeneralError)
	}
	return r.SendEnvelope(inbox)
}

func handleCreateInbox(r *fastglue.Request) error {
	var (
		app   = r.Context.(*App)
		inbox = imodels.Inbox{}
	)
	if err := r.Decode(&inbox, "json"); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "decode failed", err.Error(), envelope.InputError)
	}
	err := app.inbox.Create(inbox)
	if err != nil {
		return sendErrorEnvelope(r, err)
	}
	return r.SendEnvelope(true)
}

func handleUpdateInbox(r *fastglue.Request) error {
	var (
		app   = r.Context.(*App)
		inbox = imodels.Inbox{}
	)
	id, err := strconv.Atoi(r.RequestCtx.UserValue("id").(string))
	if err != nil || id == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"Invalid inbox `id`.", nil, envelope.InputError)
	}

	if err := r.Decode(&inbox, "json"); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "decode failed", err.Error(), envelope.InputError)
	}
	err = app.inbox.Update(id, inbox)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Could not update inbox.", nil, envelope.GeneralError)
	}
	return r.SendEnvelope(inbox)
}

func handleToggleInbox(r *fastglue.Request) error {
	var (
		app = r.Context.(*App)
	)
	id, err := strconv.Atoi(r.RequestCtx.UserValue("id").(string))
	if err != nil || id == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"Invalid inbox `id`.", nil, envelope.InputError)
	}

	if err = app.inbox.Toggle(id); err != nil {
		return err
	}
	return r.SendEnvelope(true)
}

func handleDeleteInbox(r *fastglue.Request) error {
	var (
		app   = r.Context.(*App)
		id, _ = strconv.Atoi(r.RequestCtx.UserValue("id").(string))
	)
	err := app.inbox.Delete(id)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Could not update inbox.", nil, envelope.GeneralError)
	}
	return r.SendEnvelope(true)
}
