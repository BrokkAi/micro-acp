package client

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BrokkAi/acp-go/schema"
)

type Choice struct{ Value, Name, Description, Group string }
type Selector struct {
	ID, Name, Description, Category, Current string
	Boolean                                  bool
	Choices                                  []Choice
}

func ptrText[T ~string](s *T) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func selectChoices(raw any) []Choice {
	b, _ := json.Marshal(raw)
	var entries []json.RawMessage
	if json.Unmarshal(b, &entries) != nil {
		return nil
	}
	var choices []Choice
	for _, entry := range entries {
		var group schema.SessionConfigSelectGroup
		_ = json.Unmarshal(entry, &group)
		if group.Group != "" {
			for _, option := range group.Options {
				choices = append(choices, Choice{string(option.Value), option.Name, ptrText(option.Description), group.Name})
			}
		} else {
			var option schema.SessionConfigSelectOption
			if json.Unmarshal(entry, &option) == nil {
				choices = append(choices, Choice{string(option.Value), option.Name, ptrText(option.Description), ""})
			}
		}
	}
	return choices
}

// Selectors preserves the agent's ordering and prefers configOptions over legacy modes.
func (c *Client) Selectors() []Selector {
	w := c.session()
	var selectors []Selector
	mode := false
	for _, option := range w.ConfigOptions {
		s := Selector{ID: string(option.ID), Name: option.Name, Description: ptrText(option.Description), Category: ptrText(option.Category)}
		if s.Category == "" {
			switch s.ID {
			case "model", "mode":
				s.Category = s.ID
			case "reasoning_effort":
				s.Category = "thought_level"
			}
		}
		mode = mode || s.Category == "mode"
		if option.Select != nil {
			s.Current = string(option.Select.CurrentValue)
			s.Choices = selectChoices(option.Select.Options)
		}
		if option.Boolean != nil {
			s.Boolean = true
			s.Current = strconv.FormatBool(option.Boolean.CurrentValue)
			s.Choices = []Choice{{Value: "true", Name: "On"}, {Value: "false", Name: "Off"}}
		}
		selectors = append(selectors, s)
	}
	if !mode && w.Modes != nil {
		s := Selector{ID: "@mode", Name: "Mode", Category: "mode", Current: string(w.Modes.CurrentModeID)}
		for _, mode := range w.Modes.AvailableModes {
			s.Choices = append(s.Choices, Choice{Value: string(mode.ID), Name: mode.Name, Description: ptrText(mode.Description)})
		}
		selectors = append(selectors, s)
	}
	return selectors
}

func (c *Client) Configure(kind, value string) error {
	category := kind
	if kind == "effort" {
		category = "thought_level"
	}
	for _, s := range c.Selectors() {
		if s.Category == category || s.ID == kind {
			return c.SetConfig(s.ID, value)
		}
	}
	return fmt.Errorf("agent does not offer %s selection", kind)
}

func (c *Client) SetConfig(id, value string) error {
	c.op.Lock()
	defer c.op.Unlock()
	w := c.session()
	if w.SessionID == "" {
		return fmt.Errorf("create or load a session first")
	}
	var selected *Selector
	for _, s := range c.Selectors() {
		if s.ID == id {
			selected = &s
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("unknown configuration %q", id)
	}
	valid := false
	for _, choice := range selected.Choices {
		if choice.Value == value {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("%q is not offered for %s", value, selected.Name)
	}
	ctx, cancel := c.operation()
	defer cancel()
	if id == "@mode" {
		if err := c.conn.SetMode(ctx, &w, value); err != nil {
			return err
		}
		c.mu.Lock()
		c.wire.Modes = w.Modes
		c.revision++
		c.mu.Unlock()
		return nil
	}
	request := schema.SetSessionConfigOptionRequest{SessionID: w.SessionID, ConfigID: schema.SessionConfigId(id)}
	if selected.Boolean {
		request.Boolean = &schema.SetSessionConfigOptionRequestBoolean{Value: value == "true"}
	} else {
		request.ValueID = &schema.SetSessionConfigOptionRequestValueID{Value: schema.SessionConfigValueId(value)}
	}
	var response schema.SetSessionConfigOptionResponse
	if err := c.conn.Call(ctx, schema.SessionSetConfigOptionMethodName, request, &response); err != nil {
		return err
	}
	c.mu.Lock()
	c.wire.ConfigOptions = response.ConfigOptions
	c.revision++
	c.mu.Unlock()
	for _, s := range c.Selectors() {
		if s.ID == id && s.Current == value {
			return nil
		}
	}
	return fmt.Errorf("agent did not confirm %s = %s", selected.Name, value)
}

type StatusField struct {
	Name, Value, Category string
	Boolean, Enabled      bool
	Used, Capacity        uint64
}

// StatusFields keeps presentation metadata separate from values so the TUI can
// use compact, semantic styling without parsing display strings.
func (c *Client) StatusFields() []StatusField {
	var values []StatusField
	selectors := c.Selectors()
	rank := func(s Selector) int {
		switch s.Category {
		case "model":
			return 0
		case "thought_level":
			return 1
		case "mode":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(selectors, func(i, j int) bool { return rank(selectors[i]) < rank(selectors[j]) })
	for _, s := range selectors {
		name := s.Current
		for _, o := range s.Choices {
			if o.Value == s.Current {
				name = o.Name
			}
		}
		values = append(values, StatusField{Name: s.Name, Value: name, Category: s.Category, Boolean: s.Boolean, Enabled: s.Current == "true"})
	}
	s, _ := c.Snapshot()
	if s.Usage != nil {
		values = append(values, StatusField{Value: fmt.Sprintf("%d/%d tokens", s.Usage.Used, s.Usage.Size), Category: "usage", Used: uint64(s.Usage.Used), Capacity: uint64(s.Usage.Size)})
		if s.Usage.Cost != nil {
			values = append(values, StatusField{Value: fmt.Sprintf("%.4f %s", s.Usage.Cost.Amount, s.Usage.Cost.Currency), Category: "cost"})
		}
	}
	return values
}

func (c *Client) Status() string {
	var fields []string
	for _, f := range c.StatusFields() {
		fields = append(fields, strings.TrimSpace(f.Name+" "+f.Value))
	}
	return strings.Join(fields, " · ")
}
