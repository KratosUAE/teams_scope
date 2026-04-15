package api

import "log/slog"

// NewServiceFromReaders is a test-only constructor that exposes the
// otherwise-private newServiceFromDeps so MCP-layer tests (and any future
// external test package) can build a Service from fake readers without
// spinning up a real Mongo client. Production code MUST use NewService.
//
// The parameter types are the same unexported reader interfaces the rest of
// this package uses; callers in external test packages satisfy them with
// their own fakes by matching the method sets.
func NewServiceFromReaders(
	calls callsReader,
	streams streamsReader,
	users usersReader,
	meta metaReader,
	subnets subnetsStore,
	userCards userCardsStore,
	pinger mongoPinger,
	log *slog.Logger,
) *Service {
	return newServiceFromDeps(calls, streams, users, meta, subnets, userCards, pinger, log)
}
