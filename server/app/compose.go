package app

import (
	"github.com/arhuman/maping/server/internal/auth"
	"github.com/arhuman/maping/server/internal/web"
)

// These aliases re-export the auth and web composition interfaces as a PUBLIC
// surface. A composing build lives in its own module and cannot import
// server/internal/* (the internal rule), so without these aliases it could not
// name the types WithLoginInterceptor and WithMemberAdmin require. They are type
// aliases, so a value written against the app alias IS the internal type — the
// auth callback and the Setup team panel consume it with no adapter.
type (
	// LoginInterceptor is the post-authentication hook the OIDC callback consults
	// before the default first-login path. A composing build implements it to bind
	// an identity resolved out of band (e.g. an accepted invite) and finish login.
	LoginInterceptor = auth.LoginInterceptor
	// PostAuthContext is the capability a LoginInterceptor receives to start a
	// dashboard session for the member it resolved.
	PostAuthContext = auth.PostAuthContext
	// MemberAdmin is the self-serve team surface (members + invites) the Setup page
	// renders. A composing build supplies one to expose the team panel.
	MemberAdmin = web.MemberAdmin
	// MemberInfo is a listed org member rendered in the team panel.
	MemberInfo = web.MemberInfo
	// InviteInfo is a listed pending invite rendered in the team panel.
	InviteInfo = web.InviteInfo
)
