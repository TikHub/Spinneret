package proxyapi

import (
	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/proxy"
)

// toProto converts a proxy view. The view never holds credentials.
func toProto(v *proxy.Proxy) *spinneretv1.Proxy {
	if v == nil {
		return nil
	}
	out := &spinneretv1.Proxy{
		Id:                       v.ID,
		Namespace:                v.NamespaceName,
		DisplayUrl:               v.DisplayURL,
		Scheme:                   v.Scheme,
		Host:                     v.Host,
		Port:                     int32(v.Port),
		UsernameHint:             v.UsernameHint,
		Kind:                     v.Attributes.Kind,
		Region:                   v.Attributes.Region,
		City:                     v.Attributes.City,
		Provider:                 v.Attributes.Provider,
		MaxConcurrency:           int32(v.Attributes.MaxConcurrency),
		Tags:                     v.Attributes.Tags,
		SessionTemplate:          v.Attributes.SessionTemplate,
		State:                    v.State,
		StateReason:              v.StateReason,
		StateChangedAt:           apiutil.Timestamp(v.StateChangedAt),
		BanUntil:                 apiutil.TimestampPtr(v.BanUntil),
		CooldownUntil:            apiutil.TimestampPtr(v.CooldownUntil),
		LastCheckAt:              apiutil.TimestampPtr(v.LastCheckAt),
		LastCheckOk:              v.LastCheckOK,
		LastLatencyMs:            int32(v.LastLatencyMs),
		ExitIp:                   v.ExitIP,
		ConsecutiveCheckFailures: int32(v.ConsecutiveCheckFailures),
		BoundIdentities:          int32(v.BoundIdentities),
		Sites:                    make([]*spinneretv1.ProxySiteState, len(v.Sites)),
		CreatedAt:                apiutil.Timestamp(v.CreatedAt),
		UpdatedAt:                apiutil.Timestamp(v.UpdatedAt),
	}
	for i, s := range v.Sites {
		out.Sites[i] = &spinneretv1.ProxySiteState{
			Site:          s.Site,
			SiteId:        s.SiteID,
			State:         s.State,
			Score:         s.Score,
			Samples:       int32(s.Samples),
			ActiveLeases:  int32(s.ActiveLeases),
			CooldownUntil: apiutil.TimestampPtr(s.CooldownUntil),
		}
	}
	return out
}

// bulkToProto converts a bulk result.
func bulkToProto(r proxy.BulkResult) *spinneretv1.BulkResult {
	out := &spinneretv1.BulkResult{
		Matched:   int32(r.Matched),
		Succeeded: int32(r.Succeeded),
		Failed:    make([]*spinneretv1.BulkFailure, len(r.Failed)),
	}
	for i, f := range r.Failed {
		out.Failed[i] = &spinneretv1.BulkFailure{Id: f.ID, Reason: f.Reason, Message: f.Message}
	}
	return out
}
