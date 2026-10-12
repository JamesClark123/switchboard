package secret

import (
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxtable"
)

// parseList reads the `sbx secret ls` table. The documented header is
// `SCOPE  TYPE  NAME  SECRET` (older docs: `SCOPE  SERVICE  SECRET`); columns
// are matched by name so either shape, and extra columns, parse. The scope
// `(global)` is normalized to "global"; a missing TYPE column means "service".
func parseList(out string) []*pb.Secret {
	cols, rows := sbxtable.Parse(out, func(first string) bool {
		return first == "SCOPE" || first == "NAME" || first == "SERVICE"
	})
	var secrets []*pb.Secret
	for _, vals := range rows {
		name := sbxtable.Value(cols, vals, "NAME", "SERVICE")
		if name == "" {
			continue
		}
		typ := sbxtable.Value(cols, vals, "TYPE", "KIND")
		if typ == "" {
			typ = "service"
		}
		secrets = append(secrets, &pb.Secret{
			Scope:       normalizeScope(sbxtable.Value(cols, vals, "SCOPE")),
			Type:        strings.ToLower(typ),
			Name:        name,
			MaskedValue: sbxtable.Value(cols, vals, "SECRET", "VALUE"),
		})
	}
	return secrets
}

// normalizeScope maps the runtime's scope cell to "global" or the sandbox name.
func normalizeScope(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(strings.Trim(s, "()")) {
	case "", "global", "-":
		return "global"
	}
	return strings.Trim(s, "()")
}
