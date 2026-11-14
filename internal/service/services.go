package service

import (
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/ws"
)

// Services = kumpulan service. Tiap service memegang *Services untuk memanggil service lain.
type Services struct {
	*Deps
	dummyHash string

	Notifier    *Notifier
	Audit       *Auditor
	Fraud       *Fraud
	Legal       *LegalService
	Auth        *AuthService
	Organizer   *OrganizerService
	Media       *MediaService
	Venue       *VenueService
	Artist      *ArtistService
	Event       *EventService
	Tier        *TierService
	Phase       *PhaseService
	Seatmap     *SeatmapService
	GateAdmin   *GateAdminService
	Follow      *FollowService
	Catalog     *CatalogService
	Analytics   *AnalyticsService
	Queue       *QueueService
	Hold        *HoldService
	Waitlist    *WaitlistService
	Ledger      *LedgerService
	Ticket      *TicketService
	Promo       *PromoService
	Voucher     *VoucherService
	Order       *OrderService
	Payment     *PaymentService
	Invoice     *InvoiceService
	Refund      *RefundService
	Gate        *GateService
	Transfer    *TransferService
	Lifecycle   *LifecycleService
	Finance     *FinanceService
	Ops         *OpsService
	Attendee    *AttendeeService
	Account     *AccountService
	Privacy     *PrivacyService
	Support     *SupportService
	SEO         *SEOService
	WS          *ws.Hub
	Integration *IntegrationService
	CMS         *CMSService
	Assist      *SeatAssist
	Plan        *PlanService
	Ready       *ReadinessService
	AdminUsers  *AdminUsers
	Passport    *PassportService
	Reviews     *ReviewService
	Concierge   *Concierge
	Resale      *ResaleService
	Squad       *SquadService
	AdminOrders *AdminOrders
}

// New merakit semua service. Handler job didaftarkan ke d.Reg.
func New(d *Deps) *Services {
	if d.Reg == nil {
		d.Reg = jobs.NewRegistry()
	}
	s := &Services{Deps: d}
	s.dummyHash = d.Hasher.Hash("dummy-password-for-timing-equalization")
	s.Notifier = newNotifier(s)
	s.Audit = newAuditor(s)
	s.Fraud = newFraud(s)
	s.Legal = newLegalService(s)
	s.Auth = newAuthService(s)
	s.Organizer = newOrganizerService(s)
	s.Media = newMediaService(s)
	s.Venue = newVenueService(s)
	s.Artist = newArtistService(s)
	s.Event = newEventService(s)
	s.Tier = newTierService(s)
	s.Phase = newPhaseService(s)
	s.Seatmap = newSeatmapService(s)
	s.GateAdmin = newGateAdminService(s)
	s.Follow = newFollowService(s)
	s.Catalog = newCatalogService(s)
	s.Analytics = newAnalyticsService(s)
	s.Queue = newQueueService(s)
	s.Hold = newHoldService(s)
	s.Waitlist = newWaitlistService(s)
	s.Ledger = newLedgerService(s)
	s.Ticket = newTicketService(s)
	s.Promo = newPromoService(s)
	s.Voucher = newVoucherService(s)
	s.Invoice = newInvoiceService(s)
	s.Refund = newRefundService(s)
	s.Order = newOrderService(s)
	s.Payment = newPaymentService(s)
	s.Gate = newGateService(s)
	s.Transfer = newTransferService(s)
	s.Lifecycle = newLifecycleService(s)
	s.Finance = newFinanceService(s)
	s.Ops = newOpsService(s)
	s.Attendee = newAttendeeService(s)
	s.Account = newAccountService(s)
	s.Privacy = newPrivacyService(s)
	s.Support = newSupportService(s)
	s.SEO = newSEOService(s)
	s.Integration = newIntegrationService(s)
	s.CMS = newCMSService(s)
	s.Assist = newSeatAssist(s)
	s.Plan = newPlanService(s)
	s.Ready = newReadinessService(s)
	s.AdminUsers = newAdminUsers(s)
	s.Passport = newPassportService(s)
	s.Reviews = newReviewService(s)
	s.Concierge = newConcierge(s)
	s.Resale = newResaleService(s)
	s.Squad = newSquadService(s)
	s.AdminOrders = newAdminOrders(s)
	s.WS = ws.New(d.Redis, BusChannel, s.AuthorizeChannel, d.Cfg.CORSOrigins, d.Log)
	return s
}
