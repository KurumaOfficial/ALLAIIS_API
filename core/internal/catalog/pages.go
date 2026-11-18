// SPDX-License-Identifier: GPL-3.0-only

package catalog

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

type wireModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	ModelID     string   `json:"model"`
	DisplayName string   `json:"display_name"`
	GoogleName  string   `json:"displayName"`
	Type        string   `json:"type"`
	Methods     []string `json:"supportedGenerationMethods"`
	Spec        struct {
		Name string `json:"name"`
	} `json:"model_spec"`
}

func (m wireModel) model(format config.CatalogFormat) Model {
	id, name := m.ID, m.Name
	switch format {
	case config.ClaudeCatalog:
		name = m.DisplayName
	case config.GeminiCatalog:
		id, name = strings.TrimPrefix(m.Name, "models/"), m.GoogleName
	case config.DashScopeCatalog:
		id = m.ModelID
	default:
		if name == "" {
			name = m.Spec.Name
		}
	}
	if name == "" {
		name = id
	}
	return Model{ID: id, Name: name, Type: m.Type, Methods: m.Methods}
}

type pagination struct {
	query url.Values
	rows  int
}

func (p *pagination) decode(body []byte, format config.CatalogFormat) ([]wireModel, bool, error) {
	var page struct {
		Data      []wireModel `json:"data"`
		HasMore   bool        `json:"has_more"`
		LastID    string      `json:"last_id"`
		Models    []wireModel `json:"models"`
		NextToken string      `json:"nextPageToken"`
		Success   bool        `json:"success"`
		Output    struct {
			Total  int         `json:"total"`
			Page   int         `json:"page_no"`
			Models []wireModel `json:"models"`
		} `json:"output"`
	}
	invalid := errors.New("provider returned an invalid model catalog")
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, false, invalid
	}
	rows, more := page.Data, false
	switch format {
	case config.ClaudeCatalog:
		if page.HasMore {
			if page.LastID == "" || len(rows) == 0 {
				return nil, false, invalid
			}
			p.query.Set("after_id", page.LastID)
			more = true
		}
	case config.GeminiCatalog:
		rows = page.Models
		if page.NextToken != "" {
			p.query.Set("pageToken", page.NextToken)
			more = true
		}
	case config.DashScopeCatalog:
		number, _ := strconv.Atoi(p.query.Get("page_no"))
		rows = page.Output.Models
		if !page.Success || page.Output.Page != number || page.Output.Total < 0 {
			return nil, false, invalid
		}
		more = p.rows+len(rows) < page.Output.Total
		if more {
			if len(rows) == 0 {
				return nil, false, invalid
			}
			p.query.Set("page_no", strconv.Itoa(number+1))
		}
	}
	if rows == nil {
		return nil, false, invalid
	}
	p.rows += len(rows)
	return rows, more, nil
}
