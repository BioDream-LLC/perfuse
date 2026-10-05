package dashboards

// Grafana renders a dashboard as Grafana dashboard JSON over Perfuse's /metrics, for import into a Grafana whose Prometheus
// scrapes Perfuse. Tiles with no metric are left out and listed in the description, so the export says what it lacks.
//
// The data source is a template variable rather than a name, so the file imports into any Grafana; ${channel} filters every
// panel whose query has a channel label.
func Grafana(d Dashboard, tiles []string) map[string]any {
	if len(tiles) == 0 {
		tiles = d.Tiles
	}
	var panels []any
	var left []string
	y := 0
	for i, id := range tiles {
		t, ok := TileByID(id)
		if !ok {
			continue
		}
		if t.Query == "" {
			left = append(left, t.Title)
			continue
		}
		query := withChannel(t.Query)
		panels = append(panels, map[string]any{
			"id":          i + 1,
			"type":        "timeseries",
			"title":       t.Title,
			"description": t.Description,
			"datasource":  map[string]any{"type": "prometheus", "uid": "${datasource}"},
			"gridPos":     map[string]any{"x": (len(panels) % 2) * 12, "y": y, "w": 12, "h": 8},
			"fieldConfig": map[string]any{"defaults": map[string]any{"unit": t.Unit}, "overrides": []any{}},
			"targets":     []any{map[string]any{"refId": "A", "expr": query, "datasource": map[string]any{"type": "prometheus", "uid": "${datasource}"}}},
		})
		if len(panels)%2 == 0 {
			y += 8
		}
	}
	desc := d.Description
	if len(left) > 0 {
		desc += " Not exported, because they have no metric behind them: " + join(left) + "."
	}
	return map[string]any{
		"title":         "Perfuse - " + d.Title,
		"description":   desc,
		"tags":          []string{"perfuse", d.ID},
		"schemaVersion": 39,
		"editable":      true,
		"time":          map[string]any{"from": "now-24h", "to": "now"},
		"refresh":       "1m",
		"templating": map[string]any{"list": []any{
			map[string]any{"name": "datasource", "label": "Prometheus", "type": "datasource", "query": "prometheus"},
			map[string]any{"name": "channel", "label": "Channel", "type": "query", "datasource": map[string]any{"type": "prometheus", "uid": "${datasource}"},
				"query": "label_values(perfuse_messages_received_total, channel)", "includeAll": true, "multi": true, "allValue": ".*"},
		}},
		"panels": panels,
	}
}

// withChannel adds the channel variable to every perfuse_ selector in a query.
func withChannel(q string) string {
	out := []byte{}
	for i := 0; i < len(q); {
		j := indexFrom(q, "perfuse_", i)
		if j < 0 {
			out = append(out, q[i:]...)
			break
		}
		out = append(out, q[i:j]...)
		k := j
		for k < len(q) && (q[k] == '_' || (q[k] >= 'a' && q[k] <= 'z')) {
			k++
		}
		out = append(out, q[j:k]...)
		if k < len(q) && q[k] == '{' {
			out = append(out, `{channel=~"$channel",`...)
			k++
		} else {
			out = append(out, `{channel=~"$channel"}`...)
		}
		i = k
	}
	return string(out)
}

func indexFrom(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
