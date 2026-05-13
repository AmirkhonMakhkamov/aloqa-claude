package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	wshandler "aloqa/internal/handler/ws"
	"aloqa/internal/middleware"
)

type RouterDeps struct {
	Auth             *AuthHandler
	Account          *AccountHandler
	Channels         *ChannelHandler
	Messages         *MessageHandler
	Calls            *CallHandler
	Calendar         *CalendarHandler
	Breakout         *BreakoutHandler
	Files            *FileHandler
	Presence         *PresenceHandler
	Recordings       *RecordingHandler
	Notifications    *NotificationHandler
	Search           *SearchHandler
	Admin            *AdminHandler
	Metrics          *MetricsHandler
	Guests           *GuestHandler
	WS               *wshandler.Handler
	Validator        middleware.TokenValidator
	PersonalResolver middleware.PersonalWorkspaceResolver
	Idempotency      func(http.Handler) http.Handler
	CORSOrigins      []string // Allowed CORS origins from config
	RequestMetrics   *middleware.RequestMetricsCollector
}

func NewRouter(deps RouterDeps) *chi.Mux {
	r := chi.NewRouter()

	// Global middleware.
	r.Use(middleware.RequestID)
	r.Use(middleware.RequestLogger)
	if deps.RequestMetrics != nil {
		r.Use(middleware.RequestMetrics(deps.RequestMetrics))
	}
	r.Use(middleware.Recover)
	r.Use(middleware.SecureHeaders)
	corsConfig := middleware.DefaultCORSConfig()
	if len(deps.CORSOrigins) > 0 {
		corsConfig.AllowedOrigins = deps.CORSOrigins
		corsConfig.AllowCredentials = true
	}
	r.Use(middleware.CORS(corsConfig))
	r.Use(middleware.BodyLimit(128 << 20)) // Supports the 100 MB default media upload limit plus multipart overhead.
	r.Use(chimw.RealIP)
	// Global per-IP cap. The previous 100/min was DoS-tier — a single user
	// loading a workspace fires accountApi.me, workspaces.list, channels.list,
	// channels.unread, presence.list, notifications.list, messages.list per
	// channel, and a media-session token + offer + ~10 ICE candidates per
	// call. That's easily 30+ requests on first paint, so 100/min trips
	// inside a few minutes of normal use and surfaces as cryptic spinners
	// (chat "Loading messages…") or call-signaling failures ("Too Many
	// Requests" → engine.join() throws → engineState=failed). Keep a wide
	// per-IP guard so a runaway script still gets cut off, but raise it well
	// above realistic single-user burst traffic. The auth limiter below is
	// intentionally still 10/min — credential-stuffing protection is the
	// only thing that benefits from a tight cap here.
	r.Use(httprate.LimitByIP(2400, 1*time.Minute))

	// Health and readiness probes.
	r.Get("/healthz", healthCheck)
	r.Get("/readyz", ReadinessCheck())
	if deps.Metrics != nil {
		r.Handle("/metrics", deps.Metrics)
	}

	// Public routes (no auth). Stricter per-IP rate limits apply to
	// unauthenticated endpoints to resist credential-stuffing and
	// enumeration attacks. The cap is intentionally well above legitimate
	// dev/burst traffic (opening a few tabs, running a small e2e seed
	// script) but still low enough to throttle a real brute-force attempt:
	// 60 attempts/min is ~1/sec, which is two orders of magnitude below
	// what a credential-stuffing attacker would want.
	authLimiter := httprate.LimitByIP(60, 1*time.Minute)
	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authLimiter)
			r.Post("/register", deps.Auth.Register)
			r.Post("/login", deps.Auth.Login)
			r.Post("/refresh", deps.Auth.Refresh)
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.Auth(deps.Validator))
			r.Post("/logout", deps.Auth.Logout)
			r.Post("/logout-all", deps.Auth.LogoutAll)
			r.Get("/sessions", deps.Auth.ListSessions)
		})
	})

	// Guest invite redemption (public). Share the auth limiter to prevent
	// token brute force.
	r.Group(func(r chi.Router) {
		r.Use(authLimiter)
		r.Post("/api/v1/invites/{token}/redeem", deps.Guests.RedeemInvite)
	})

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(deps.Validator))
		if deps.Idempotency != nil {
			r.Use(deps.Idempotency)
		}

		// Platform user account endpoints.
		r.Route("/api/v1/users", func(r chi.Router) {
			r.Get("/me", deps.Account.Me)
			r.Patch("/me", deps.Account.UpdateProfile)
		})

		// WebSocket endpoint.
		r.Get("/ws", deps.WS.ServeHTTP)

		// File downloads (authenticated).
		r.Get("/files/*", deps.Files.Download)

		r.Route("/api/v1/admin", func(r chi.Router) {
			r.Post("/search/reindex", deps.Admin.ReindexSearch)
			r.Post("/search/backfill-users", deps.Admin.BackfillSearchUsers)
		})

		// Workspace catalog plus workspace-scoped collaboration routes.
		r.Route("/api/v1/workspaces", func(r chi.Router) {
			r.Get("/", deps.Account.ListWorkspaces)
			r.Post("/", deps.Account.CreateWorkspace)

			r.Route("/{workspaceID}", func(r chi.Router) {
				r.Use(middleware.WorkspaceCtx)
				r.Get("/", deps.Account.GetWorkspace)
				mountWorkspaceScopedRoutes(r, deps)
			})
		})

		r.Route("/api/v1/personal", func(r chi.Router) {
			r.Use(middleware.PersonalWorkspaceCtx(deps.PersonalResolver))
			r.Get("/", deps.Account.GetPersonalWorkspace)
			mountPersonalScopedRoutes(r, deps)
		})
	})

	return r
}

func mountWorkspaceScopedRoutes(r chi.Router, deps RouterDeps) {
	mountSharedScopedRoutes(r, deps)

	// Non-admin workspace member list — used by the client to render DM
	// counterparts and the "New DM" picker without admin permissions.
	r.Get("/members", deps.Account.ListWorkspaceMembers)

	// Admin (workspace management).
	r.Route("/admin", func(r chi.Router) {
		r.Get("/members", deps.Admin.ListMembers)
		r.Post("/members", deps.Admin.InviteMember)
		r.Route("/media", func(r chi.Router) {
			r.Get("/nodes", deps.Admin.ListMediaNodes)
			r.Get("/topology", deps.Admin.MediaTopology)
			r.Get("/calls/{callID}/qos", deps.Admin.CallQoSHistory)
			r.Get("/calls/{callID}/quality-report", deps.Admin.CallQualityReport)
			r.Get("/calls/{callID}/quality-policy", deps.Admin.GetCallQualityPolicy)
			r.Put("/calls/{callID}/quality-policy", deps.Admin.UpdateCallQualityPolicy)
			r.Get("/calls/{callID}/alerts", deps.Admin.ListCallQualityAlerts)
		})
		r.Route("/storage", func(r chi.Router) {
			r.Get("/runtime", deps.Admin.StorageRuntime)
			r.Get("/audit", deps.Admin.StorageAudit)
		})
		r.Route("/observability", func(r chi.Router) {
			r.Get("/dashboard", deps.Admin.ObservabilityDashboard)
			r.Get("/alerts", deps.Admin.ObservabilityAlerts)
			r.Get("/slos", deps.Admin.ObservabilitySLOs)
		})
		r.Route("/roles", func(r chi.Router) {
			r.Get("/", deps.Admin.ListRoles)
			r.Post("/", deps.Admin.CreateRole)
			r.Route("/{roleID}", func(r chi.Router) {
				r.Put("/", deps.Admin.UpdateRole)
				r.Delete("/", deps.Admin.DeleteRole)
			})
		})
		r.Put("/settings", deps.Admin.UpdateWorkspace)
		r.Get("/audit-log", deps.Admin.AuditLog)
		r.Route("/members/{userID}", func(r chi.Router) {
			r.Put("/role", deps.Admin.UpdateMemberRole)
			r.Route("/roles", func(r chi.Router) {
				r.Get("/", deps.Admin.ListMemberRoles)
				r.Post("/{roleID}", deps.Admin.AssignMemberRole)
				r.Delete("/{roleID}", deps.Admin.UnassignMemberRole)
			})
			r.Delete("/", deps.Admin.RemoveMember)
			r.Post("/suspend", deps.Admin.SuspendUser)
			r.Post("/reactivate", deps.Admin.ReactivateUser)
		})
	})
}

func mountPersonalScopedRoutes(r chi.Router, deps RouterDeps) {
	mountSharedScopedRoutes(r, deps)
}

func mountSharedScopedRoutes(r chi.Router, deps RouterDeps) {
	// Presence.
	r.Route("/presence", func(r chi.Router) {
		r.Put("/", deps.Presence.SetStatus)
		r.Get("/", deps.Presence.ListOnline)
	})

	// Notifications.
	r.Route("/notifications", func(r chi.Router) {
		r.Get("/", deps.Notifications.List)
		r.Post("/read-all", deps.Notifications.MarkAllRead)
		r.Get("/unread-count", deps.Notifications.CountUnread)
		r.Post("/{notificationID}/read", deps.Notifications.MarkRead)
	})

	// Search.
	r.Get("/search", deps.Search.Search)

	// Guest invites.
	r.Route("/invites", func(r chi.Router) {
		r.Post("/", deps.Guests.CreateInvite)
		r.Get("/", deps.Guests.ListInvites)
		r.Delete("/{inviteID}", deps.Guests.RevokeInvite)
	})

	// Channels.
	r.Route("/channels", func(r chi.Router) {
		r.Post("/", deps.Channels.Create)
		r.Get("/", deps.Channels.List)
		r.Post("/dm", deps.Channels.CreateDM)
		r.Get("/unread", deps.Channels.UnreadCounts)

		r.Route("/{channelID}", func(r chi.Router) {
			r.Get("/", deps.Channels.Get)
			r.Put("/", deps.Channels.Update)
			r.Post("/join", deps.Channels.Join)
			r.Post("/leave", deps.Channels.Leave)
			r.Post("/read", deps.Channels.MarkRead)

			// Messages within a channel.
			r.Route("/messages", func(r chi.Router) {
				r.Post("/", deps.Messages.Send)
				r.Get("/", deps.Messages.List)
				r.Get("/pinned", deps.Messages.ListPinned)

				r.Route("/{messageID}", func(r chi.Router) {
					r.Get("/", deps.Messages.Get)
					r.Put("/", deps.Messages.Edit)
					r.Delete("/", deps.Messages.Delete)
					r.Get("/thread", deps.Messages.ListThread)
					r.Post("/reactions", deps.Messages.AddReaction)
					r.Delete("/reactions/{emoji}", deps.Messages.RemoveReaction)
					r.Post("/pin", deps.Messages.Pin)
					r.Delete("/pin", deps.Messages.Unpin)

					// File attachments.
					r.Post("/attachments", deps.Files.Upload)
				})
			})
		})
	})

	// Calls.
	r.Route("/calls", func(r chi.Router) {
		r.Post("/", deps.Calls.Start)
		r.Get("/", deps.Calls.ListActive)
		r.Get("/recents", deps.Calls.Recents)

		r.Route("/{callID}", func(r chi.Router) {
			r.Get("/", deps.Calls.Get)
			r.Post("/join", deps.Calls.Join)
			r.Post("/leave", deps.Calls.Leave)
			r.Post("/end", deps.Calls.End)
			r.Get("/participants", deps.Calls.Participants)
			r.Put("/participants/{userID}/role", deps.Calls.UpdateParticipantRole)
			r.Put("/media", deps.Calls.UpdateMedia)
			r.Put("/quality", deps.Calls.SetQuality)
			r.Route("/media-session", func(r chi.Router) {
				r.Post("/token", deps.Calls.MediaToken)
				r.Post("/offer", deps.Calls.MediaOffer)
				r.Post("/ice-candidate", deps.Calls.MediaICECandidate)
				r.Post("/ice-restart", deps.Calls.MediaICERestart)
				r.Post("/quality-report", deps.Calls.ReportNetworkQuality)
			})

			// Waiting room.
			r.Get("/waiting", deps.Calls.ListWaiting)
			r.Post("/admit", deps.Calls.Admit)
			r.Post("/admit-all", deps.Calls.AdmitAll)
			r.Post("/reject", deps.Calls.Reject)

			// Recordings.
			r.Route("/recordings", func(r chi.Router) {
				r.Post("/", deps.Recordings.Start)
				r.Get("/", deps.Recordings.ListByCall)
				r.Route("/{recordingID}", func(r chi.Router) {
					r.Get("/", deps.Recordings.Get)
					r.Post("/stop", deps.Recordings.Stop)
					r.Get("/artifacts", deps.Recordings.ListArtifacts)
					r.Get("/artifacts/{artifactID}/download", deps.Recordings.DownloadArtifact)
				})
			})

			// Breakout rooms.
			r.Route("/breakout-rooms", func(r chi.Router) {
				r.Post("/", deps.Breakout.Create)
				r.Get("/", deps.Breakout.List)
				r.Post("/return", deps.Breakout.Return)
				r.Post("/close-all", deps.Breakout.CloseAll)
				r.Post("/broadcast", deps.Breakout.Broadcast)

				r.Route("/{breakoutRoomID}", func(r chi.Router) {
					r.Post("/join", deps.Breakout.Join)
					r.Post("/close", deps.Breakout.Close)
					r.Get("/participants", deps.Breakout.Participants)
				})
			})
		})
	})

	// Calendars.
	r.Route("/calendars", func(r chi.Router) {
		r.Get("/", deps.Calendar.ListCalendars)
		r.Post("/", deps.Calendar.CreateCalendar)
		r.Route("/{calendarID}", func(r chi.Router) {
			r.Patch("/", deps.Calendar.UpdateCalendar)
			r.Delete("/", deps.Calendar.DeleteCalendar)
			r.Put("/visibility", deps.Calendar.SetCalendarVisibility)
		})
	})

	// Calendar events.
	r.Route("/events", func(r chi.Router) {
		r.Get("/", deps.Calendar.ListEvents)
		r.Post("/", deps.Calendar.CreateEvent)
		r.Get("/upcoming", deps.Calendar.ListUpcoming)
		r.Route("/{eventID}", func(r chi.Router) {
			r.Get("/", deps.Calendar.GetEvent)
			r.Patch("/", deps.Calendar.UpdateEvent)
			r.Delete("/", deps.Calendar.DeleteEvent)
			r.Put("/rsvp", deps.Calendar.UpsertRsvp)
			r.Post("/start-call", deps.Calendar.StartCallFromEvent)
		})
	})
}
