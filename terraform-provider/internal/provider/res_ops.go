// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

var (
	boolKeep = []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	intKeep  = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
)

func optFloat(dst *types.Float64, o client.Obj, key string) {
	if dst.IsNull() {
		return
	}
	if f, ok := o[key].(float64); ok {
		*dst = types.Float64Value(f)
	}
}

// ---- polipan_backup_target ----

type backupTargetModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Kind        types.String `tfsdk:"kind"`
	Config      types.String `tfsdk:"config"`
	Credentials types.Map    `tfsdk:"credentials"`
}

func backupTargetBody(m *backupTargetModel) (client.Obj, error) {
	body := client.Obj{}
	put(body, "name", m.Name)
	put(body, "kind", m.Kind)
	put(body, "credentials", m.Credentials)
	if err := putJSON(body, "config", m.Config); err != nil {
		return nil, err
	}
	return body, nil
}

var backupTargetSpec = spec[backupTargetModel]{
	name: "backup_target",
	schema: schema.Schema{
		Description: "A place where backups are stored (S3, B2, SFTP, local path, Azure or GCS).",
		Attributes: map[string]schema.Attribute{
			"id":     idAttr("The backup target id."),
			"name":   schema.StringAttribute{Required: true, Description: "Target name."},
			"kind":   schema.StringAttribute{Required: true, Description: "`s3`, `b2`, `sftp`, `local`, `azure` or `gcs`. A change replaces the target.", PlanModifiers: replaceMods},
			"config": schema.StringAttribute{Required: true, Description: "Target settings as JSON, for example `jsonencode({ bucket = \"backups\", endpoint = \"https://s3.example.com\" })`. Only the keys you write are compared."},
			"credentials": schema.MapAttribute{Optional: true, Sensitive: true, ElementType: types.StringType,
				Description: "Access keys or passwords. Write-only: the API never returns them. The state keeps the value you gave."},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *backupTargetModel) error {
		body, err := backupTargetBody(m)
		if err != nil {
			return err
		}
		o, err := c.Post(ctx, "/backup-targets", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *backupTargetModel) (bool, error) {
		o, err := c.Find(ctx, "/backup-targets", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Name, o, "name")
		setStr(&m.Kind, o, "kind")
		if m.Config.IsNull() { // import: take the whole config
			m.Config = types.StringValue(jsonText(client.Sub(o, "config")))
		} else {
			syncJSON(&m.Config, client.Sub(o, "config"))
		}
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *backupTargetModel) error {
		body, err := backupTargetBody(plan)
		if err != nil {
			return err
		}
		_, err = c.Patch(ctx, "/backup-targets/"+state.ID.ValueString(), body)
		return err
	},
	del: func(ctx context.Context, c *client.Client, m *backupTargetModel) error {
		return c.Delete(ctx, "/backup-targets/"+m.ID.ValueString())
	},
}

// ---- polipan_backup_policy ----

type backupPolicyModel struct {
	ID                   types.String `tfsdk:"id"`
	ServiceID            types.String `tfsdk:"service_id"`
	VolumeID             types.String `tfsdk:"volume_id"`
	Schedule             types.String `tfsdk:"schedule"`
	TargetIDs            types.Set    `tfsdk:"target_ids"`
	KeepDaily            types.Int64  `tfsdk:"keep_daily"`
	KeepWeekly           types.Int64  `tfsdk:"keep_weekly"`
	KeepMonthly          types.Int64  `tfsdk:"keep_monthly"`
	RestoreCheckSchedule types.String `tfsdk:"restore_check_schedule"`
	RestoreCheckEnabled  types.Bool   `tfsdk:"restore_check_enabled"`
	RestoreCheckMode     types.String `tfsdk:"restore_check_mode"`
	Enabled              types.Bool   `tfsdk:"enabled"`
	PreHook              types.List   `tfsdk:"pre_hook"`
	PostHook             types.List   `tfsdk:"post_hook"`
}

func backupPolicyBody(m *backupPolicyModel) client.Obj {
	body := client.Obj{}
	put(body, "volume_id", m.VolumeID)
	put(body, "schedule", m.Schedule)
	put(body, "target_ids", m.TargetIDs)
	put(body, "restore_check_schedule", m.RestoreCheckSchedule)
	put(body, "restore_check_enabled", m.RestoreCheckEnabled)
	put(body, "restore_check_mode", m.RestoreCheckMode)
	put(body, "enabled", m.Enabled)
	put(body, "pre_hook", m.PreHook)
	put(body, "post_hook", m.PostHook)
	ret := client.Obj{}
	put(ret, "keep_daily", m.KeepDaily)
	put(ret, "keep_weekly", m.KeepWeekly)
	put(ret, "keep_monthly", m.KeepMonthly)
	if len(ret) > 0 {
		body["retention"] = ret
	}
	return body
}

func readBackupPolicy(m *backupPolicyModel, o client.Obj) {
	setStr(&m.ServiceID, o, "service_id")
	optStr(&m.VolumeID, o, "volume_id")
	setStr(&m.Schedule, o, "schedule")
	setSet(&m.TargetIDs, o, "target_ids")
	ret := client.Sub(o, "retention")
	optInt(&m.KeepDaily, ret, "keep_daily")
	optInt(&m.KeepWeekly, ret, "keep_weekly")
	optInt(&m.KeepMonthly, ret, "keep_monthly")
	optStr(&m.RestoreCheckSchedule, o, "restore_check_schedule")
	optBool(&m.RestoreCheckEnabled, o, "restore_check_enabled")
	optStr(&m.RestoreCheckMode, o, "restore_check_mode")
	setBool(&m.Enabled, o, "enabled")
	hooks := client.Sub(o, "hooks")
	optList(&m.PreHook, hooks, "pre")
	optList(&m.PostHook, hooks, "post")
}

var backupPolicySpec = spec[backupPolicyModel]{
	name: "backup_policy",
	schema: schema.Schema{
		Description: "A backup schedule for a service (a database or its volumes).",
		Attributes: map[string]schema.Attribute{
			"id":                     idAttr("The backup policy id."),
			"service_id":             schema.StringAttribute{Required: true, Description: "The service to back up. A change replaces the policy.", PlanModifiers: replaceMods},
			"volume_id":              schema.StringAttribute{Optional: true, Description: "Back up one volume. Not needed for a database."},
			"schedule":               schema.StringAttribute{Required: true, Description: "Cron schedule, for example `0 2 * * *`."},
			"target_ids":             schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Ids of the backup targets."},
			"keep_daily":             schema.Int64Attribute{Optional: true, Description: "Daily backups to keep (server default 7)."},
			"keep_weekly":            schema.Int64Attribute{Optional: true, Description: "Weekly backups to keep (server default 4)."},
			"keep_monthly":           schema.Int64Attribute{Optional: true, Description: "Monthly backups to keep (server default 6)."},
			"restore_check_schedule": schema.StringAttribute{Optional: true, Description: "Cron schedule of the restore check."},
			"restore_check_enabled":  schema.BoolAttribute{Optional: true, Description: "Run restore checks."},
			"restore_check_mode":     schema.StringAttribute{Optional: true, Description: "`full` or `sample`."},
			"enabled":                schema.BoolAttribute{Optional: true, Computed: true, Description: "Turn the policy on or off. Default true.", PlanModifiers: boolKeep},
			"pre_hook":               schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Command to run before the backup."},
			"post_hook":              schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Command to run after the backup."},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *backupPolicyModel) error {
		o, err := c.Post(ctx, "/services/"+m.ServiceID.ValueString()+"/backup-policies", backupPolicyBody(m))
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setBool(&m.Enabled, o, "enabled")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *backupPolicyModel) (bool, error) {
		o, err := c.Get(ctx, "/backup-policies/"+m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		readBackupPolicy(m, o)
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *backupPolicyModel) error {
		o, err := c.Patch(ctx, "/backup-policies/"+state.ID.ValueString(), backupPolicyBody(plan))
		if err != nil {
			return err
		}
		setBool(&plan.Enabled, o, "enabled")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *backupPolicyModel) error {
		return c.Delete(ctx, "/backup-policies/"+m.ID.ValueString())
	},
}

// ---- polipan_alert_channel ----

type alertChannelModel struct {
	ID      types.String `tfsdk:"id"`
	Kind    types.String `tfsdk:"kind"`
	Name    types.String `tfsdk:"name"`
	Config  types.String `tfsdk:"config"`
	Secret  types.String `tfsdk:"secret"`
	Enabled types.Bool   `tfsdk:"enabled"`
}

func alertChannelBody(m *alertChannelModel) (client.Obj, error) {
	body := client.Obj{}
	put(body, "kind", m.Kind)
	put(body, "name", m.Name)
	put(body, "secret", m.Secret)
	put(body, "enabled", m.Enabled)
	if err := putJSON(body, "config", m.Config); err != nil {
		return nil, err
	}
	return body, nil
}

var alertChannelSpec = spec[alertChannelModel]{
	name: "alert_channel",
	schema: schema.Schema{
		Description: "A place where alerts are sent: Telegram, Slack, email or a webhook.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr("The alert channel id."),
			"kind":    schema.StringAttribute{Required: true, Description: "`telegram`, `slack`, `email` or `webhook`. A change replaces the channel.", PlanModifiers: replaceMods},
			"name":    schema.StringAttribute{Required: true, Description: "Channel name."},
			"config":  schema.StringAttribute{Optional: true, Description: "Channel settings as JSON, for example `jsonencode({ chat_id = \"123\" })`. Only the keys you write are compared."},
			"secret":  schema.StringAttribute{Optional: true, Sensitive: true, Description: "Bot token, webhook URL or password. Write-only: the API never returns it. The state keeps the value you gave."},
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Description: "Turn the channel on or off. Default true.", PlanModifiers: boolKeep},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *alertChannelModel) error {
		body, err := alertChannelBody(m)
		if err != nil {
			return err
		}
		o, err := c.Post(ctx, "/alert-channels", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setBool(&m.Enabled, o, "enabled")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *alertChannelModel) (bool, error) {
		o, err := c.Find(ctx, "/alert-channels", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Kind, o, "kind")
		setStr(&m.Name, o, "name")
		setBool(&m.Enabled, o, "enabled")
		syncJSON(&m.Config, client.Sub(o, "config"))
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *alertChannelModel) error {
		body, err := alertChannelBody(plan)
		if err != nil {
			return err
		}
		o, err := c.Patch(ctx, "/alert-channels/"+state.ID.ValueString(), body)
		if err != nil {
			return err
		}
		setBool(&plan.Enabled, o, "enabled")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *alertChannelModel) error {
		return c.Delete(ctx, "/alert-channels/"+m.ID.ValueString())
	},
}

// ---- polipan_alert_rule ----

type alertRuleModel struct {
	ID                    types.String  `tfsdk:"id"`
	Name                  types.String  `tfsdk:"name"`
	Type                  types.String  `tfsdk:"type"`
	Severity              types.String  `tfsdk:"severity"`
	ChannelIDs            types.Set     `tfsdk:"channel_ids"`
	ProjectID             types.String  `tfsdk:"project_id"`
	EnvironmentID         types.String  `tfsdk:"environment_id"`
	ServiceID             types.String  `tfsdk:"service_id"`
	NodeID                types.String  `tfsdk:"node_id"`
	Threshold             types.Float64 `tfsdk:"threshold"`
	ForSeconds            types.Int64   `tfsdk:"for_seconds"`
	Query                 types.String  `tfsdk:"query"`
	Count                 types.Int64   `tfsdk:"event_count"`
	RepeatIntervalSeconds types.Int64   `tfsdk:"repeat_interval_seconds"`
	Enabled               types.Bool    `tfsdk:"enabled"`
}

func alertRuleBody(m *alertRuleModel) client.Obj {
	body := client.Obj{}
	put(body, "name", m.Name)
	put(body, "type", m.Type)
	put(body, "severity", m.Severity)
	put(body, "channel_ids", m.ChannelIDs)
	put(body, "repeat_interval_seconds", m.RepeatIntervalSeconds)
	put(body, "enabled", m.Enabled)
	scope := client.Obj{}
	put(scope, "project_id", m.ProjectID)
	put(scope, "environment_id", m.EnvironmentID)
	put(scope, "service_id", m.ServiceID)
	put(scope, "node_id", m.NodeID)
	body["scope"] = scope
	params := client.Obj{}
	put(params, "threshold", m.Threshold)
	put(params, "for_seconds", m.ForSeconds)
	put(params, "query", m.Query)
	put(params, "count", m.Count)
	body["params"] = params
	return body
}

func readAlertRule(m *alertRuleModel, o client.Obj) {
	setStr(&m.Name, o, "name")
	setStr(&m.Type, o, "type")
	setStr(&m.Severity, o, "severity")
	setSet(&m.ChannelIDs, o, "channel_ids")
	setInt(&m.RepeatIntervalSeconds, o, "repeat_interval_seconds")
	setBool(&m.Enabled, o, "enabled")
	scope, params := client.Sub(o, "scope"), client.Sub(o, "params")
	optStr(&m.ProjectID, scope, "project_id")
	optStr(&m.EnvironmentID, scope, "environment_id")
	optStr(&m.ServiceID, scope, "service_id")
	optStr(&m.NodeID, scope, "node_id")
	optFloat(&m.Threshold, params, "threshold")
	optInt(&m.ForSeconds, params, "for_seconds")
	optStr(&m.Query, params, "query")
	optInt(&m.Count, params, "count")
}

var alertRuleSpec = spec[alertRuleModel]{
	name: "alert_rule",
	schema: schema.Schema{
		Description: "A rule that sends an alert to channels, for example when a node is down or the CPU is high.",
		Attributes: map[string]schema.Attribute{
			"id":                      idAttr("The alert rule id."),
			"name":                    schema.StringAttribute{Required: true, Description: "Rule name."},
			"type":                    schema.StringAttribute{Required: true, Description: "Rule type, for example `node_down` or `cpu_high`."},
			"severity":                schema.StringAttribute{Required: true, Description: "`info`, `warning` or `critical`."},
			"channel_ids":             schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Ids of the alert channels."},
			"project_id":              schema.StringAttribute{Optional: true, Description: "Scope: one project."},
			"environment_id":          schema.StringAttribute{Optional: true, Description: "Scope: one environment."},
			"service_id":              schema.StringAttribute{Optional: true, Description: "Scope: one service."},
			"node_id":                 schema.StringAttribute{Optional: true, Description: "Scope: one node."},
			"threshold":               schema.Float64Attribute{Optional: true, Description: "Threshold of the rule."},
			"for_seconds":             schema.Int64Attribute{Optional: true, Description: "The condition must hold this long."},
			"query":                   schema.StringAttribute{Optional: true, Description: "Log query, for rules on log lines."},
			"event_count":             schema.Int64Attribute{Optional: true, Description: "Number of events, for rules on log lines."},
			"repeat_interval_seconds": schema.Int64Attribute{Optional: true, Computed: true, Description: "Seconds between repeated alerts. Server default 14400.", PlanModifiers: intKeep},
			"enabled":                 schema.BoolAttribute{Optional: true, Computed: true, Description: "Turn the rule on or off. Default true.", PlanModifiers: boolKeep},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *alertRuleModel) error {
		o, err := c.Post(ctx, "/alert-rules", alertRuleBody(m))
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setInt(&m.RepeatIntervalSeconds, o, "repeat_interval_seconds")
		setBool(&m.Enabled, o, "enabled")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *alertRuleModel) (bool, error) {
		o, err := c.Find(ctx, "/alert-rules", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		readAlertRule(m, o)
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *alertRuleModel) error {
		o, err := c.Patch(ctx, "/alert-rules/"+state.ID.ValueString(), alertRuleBody(plan))
		if err != nil {
			return err
		}
		setInt(&plan.RepeatIntervalSeconds, o, "repeat_interval_seconds")
		setBool(&plan.Enabled, o, "enabled")
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *alertRuleModel) error {
		return c.Delete(ctx, "/alert-rules/"+m.ID.ValueString())
	},
}

// ---- polipan_team ----

type teamModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	MemberIDs types.Set    `tfsdk:"member_ids"`
}

// setTeamMembers makes the members of a team equal to want. A null set means: not managed.
func setTeamMembers(ctx context.Context, c *client.Client, teamID string, have, want types.Set) error {
	if want.IsNull() || want.IsUnknown() {
		return nil
	}
	cur := map[string]bool{}
	for _, v := range stringsOf(have.Elements()) {
		cur[v] = true
	}
	next := map[string]bool{}
	for _, v := range stringsOf(want.Elements()) {
		next[v] = true
		if !cur[v] {
			if _, err := c.Put(ctx, "/teams/"+teamID+"/members/"+v, nil); err != nil {
				return err
			}
		}
	}
	for v := range cur {
		if !next[v] {
			if err := c.Delete(ctx, "/teams/"+teamID+"/members/"+v); err != nil {
				return err
			}
		}
	}
	return nil
}

var teamSpec = spec[teamModel]{
	name: "team",
	schema: schema.Schema{
		Description: "A team of users. Teams can get a role in a project.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttr("The team id."),
			"name": schema.StringAttribute{Required: true, Description: "Team name."},
			"member_ids": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				Description: "User ids of the members. When not set, the members are not managed by Terraform."},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *teamModel) error {
		o, err := c.Post(ctx, "/teams", client.Obj{"name": m.Name.ValueString()})
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		return setTeamMembers(ctx, c, m.ID.ValueString(), types.SetNull(types.StringType), m.MemberIDs)
	},
	read: func(ctx context.Context, c *client.Client, m *teamModel) (bool, error) {
		o, err := c.Find(ctx, "/teams", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Name, o, "name")
		optSet(&m.MemberIDs, o, "member_ids")
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *teamModel) error {
		id := state.ID.ValueString()
		if !plan.Name.Equal(state.Name) {
			if _, err := c.Patch(ctx, "/teams/"+id, client.Obj{"name": plan.Name.ValueString()}); err != nil {
				return err
			}
		}
		return setTeamMembers(ctx, c, id, state.MemberIDs, plan.MemberIDs)
	},
	del: func(ctx context.Context, c *client.Client, m *teamModel) error {
		return c.Delete(ctx, "/teams/"+m.ID.ValueString())
	},
}

// ---- polipan_member ----

type memberModel struct {
	ID        types.String `tfsdk:"id"`
	Email     types.String `tfsdk:"email"`
	Role      types.String `tfsdk:"role"`
	Status    types.String `tfsdk:"status"`
	InviteURL types.String `tfsdk:"invite_url"`
}

var memberSpec = spec[memberModel]{
	name: "member",
	schema: schema.Schema{
		Description: "A member of the organization. Terraform invites the user by email. The invite link is in the sensitive attribute `invite_url`. " +
			"Destroy removes the member or cancels the invitation.",
		Attributes: map[string]schema.Attribute{
			"id":     idAttr("The user id, or the invitation id (starts with `inv_`) until the user accepts."),
			"email":  schema.StringAttribute{Required: true, Description: "Email of the user, in lower case. A change replaces the member.", PlanModifiers: replaceMods},
			"role":   schema.StringAttribute{Required: true, Description: "`owner`, `admin`, `developer` or `viewer`."},
			"status": schema.StringAttribute{Computed: true, Description: "`invited`, `active` or `disabled`.", PlanModifiers: computedKeep},
			"invite_url": schema.StringAttribute{Computed: true, Sensitive: true, Description: "The invite link. The API shows it only once, so it is kept in the state.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *memberModel) error {
		o, err := c.Post(ctx, "/members/invitations", client.Obj{"email": m.Email.ValueString(), "role": m.Role.ValueString()})
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "user_id")
		setStr(&m.Status, o, "status")
		setStr(&m.InviteURL, o, "invite_url")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *memberModel) (bool, error) {
		o, err := c.Find(ctx, "/members", "user_id", m.ID.ValueString())
		if client.IsNotFound(err) && !m.Email.IsNull() {
			// An accepted invitation gets the id of the new user: look for the email.
			items, lerr := c.List(ctx, "/members")
			if lerr != nil {
				return false, lerr
			}
			for _, it := range items {
				if strings.EqualFold(client.Str(it, "email"), m.Email.ValueString()) {
					o, err = it, nil
					break
				}
			}
		}
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.ID, o, "user_id")
		setStr(&m.Email, o, "email")
		setStr(&m.Role, o, "role")
		setStr(&m.Status, o, "status")
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *memberModel) error {
		_, err := c.Patch(ctx, "/members/"+state.ID.ValueString(), client.Obj{"role": plan.Role.ValueString()})
		return err
	},
	del: func(ctx context.Context, c *client.Client, m *memberModel) error {
		return c.Delete(ctx, "/members/"+m.ID.ValueString())
	},
}

// ---- polipan_sso_provider ----

type ssoModel struct {
	ID              types.String `tfsdk:"id"`
	Kind            types.String `tfsdk:"kind"`
	Name            types.String `tfsdk:"name"`
	IssuerURL       types.String `tfsdk:"issuer_url"`
	ClientID        types.String `tfsdk:"client_id"`
	ClientSecret    types.String `tfsdk:"client_secret"`
	Scopes          types.List   `tfsdk:"scopes"`
	GroupsClaim     types.String `tfsdk:"groups_claim"`
	AllowedDomains  types.Set    `tfsdk:"allowed_domains"`
	DefaultRole     types.String `tfsdk:"default_role"`
	AutoCreateUsers types.Bool   `tfsdk:"auto_create_users"`
	Enabled         types.Bool   `tfsdk:"enabled"`
}

func ssoBody(m *ssoModel) client.Obj {
	body := client.Obj{}
	put(body, "kind", m.Kind)
	put(body, "name", m.Name)
	put(body, "issuer_url", m.IssuerURL)
	put(body, "client_id", m.ClientID)
	put(body, "client_secret", m.ClientSecret)
	put(body, "scopes", m.Scopes)
	put(body, "groups_claim", m.GroupsClaim)
	put(body, "allowed_domains", m.AllowedDomains)
	put(body, "default_role", m.DefaultRole)
	put(body, "auto_create_users", m.AutoCreateUsers)
	put(body, "enabled", m.Enabled)
	return body
}

func ssoComputed(m *ssoModel, o client.Obj) {
	setStr(&m.DefaultRole, o, "default_role")
	setBool(&m.AutoCreateUsers, o, "auto_create_users")
	setBool(&m.Enabled, o, "enabled")
}

var ssoProviderSpec = spec[ssoModel]{
	name: "sso_provider",
	schema: schema.Schema{
		Description: "A single sign-on provider (OIDC, Google or GitHub).",
		Attributes: map[string]schema.Attribute{
			"id":                idAttr("The SSO provider id."),
			"kind":              schema.StringAttribute{Required: true, Description: "`oidc`, `google` or `github`. A change replaces the provider.", PlanModifiers: replaceMods},
			"name":              schema.StringAttribute{Required: true, Description: "Name shown on the login page."},
			"issuer_url":        schema.StringAttribute{Optional: true, Description: "OIDC issuer URL. Needed for `oidc`."},
			"client_id":         schema.StringAttribute{Required: true, Description: "OAuth client id."},
			"client_secret":     schema.StringAttribute{Optional: true, Sensitive: true, Description: "OAuth client secret. Write-only: the API never returns it. The state keeps the value you gave."},
			"scopes":            schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "OIDC scopes."},
			"groups_claim":      schema.StringAttribute{Optional: true, Description: "Name of the claim with the groups of the user."},
			"allowed_domains":   schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Only users with an email in these domains can log in."},
			"default_role":      schema.StringAttribute{Optional: true, Computed: true, Description: "Role of a new SSO user. Server default `viewer`.", PlanModifiers: computedKeep},
			"auto_create_users": schema.BoolAttribute{Optional: true, Computed: true, Description: "Create a user at the first login.", PlanModifiers: boolKeep},
			"enabled":           schema.BoolAttribute{Optional: true, Computed: true, Description: "Turn the provider on or off.", PlanModifiers: boolKeep},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *ssoModel) error {
		o, err := c.Post(ctx, "/sso-providers", ssoBody(m))
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		ssoComputed(m, o)
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *ssoModel) (bool, error) {
		o, err := c.Find(ctx, "/sso-providers", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		setStr(&m.Kind, o, "kind")
		setStr(&m.Name, o, "name")
		setStr(&m.ClientID, o, "client_id")
		optStr(&m.IssuerURL, o, "issuer_url")
		optList(&m.Scopes, o, "scopes")
		optStr(&m.GroupsClaim, o, "groups_claim")
		optSet(&m.AllowedDomains, o, "allowed_domains")
		ssoComputed(m, o)
		return true, nil
	},
	update: func(ctx context.Context, c *client.Client, plan, state *ssoModel) error {
		o, err := c.Patch(ctx, "/sso-providers/"+state.ID.ValueString(), ssoBody(plan))
		if err != nil {
			return err
		}
		ssoComputed(plan, o)
		return nil
	},
	del: func(ctx context.Context, c *client.Client, m *ssoModel) error {
		return c.Delete(ctx, "/sso-providers/"+m.ID.ValueString())
	},
}

// ---- polipan_api_token ----

type apiTokenModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Scopes      types.Set    `tfsdk:"scopes"`
	ProjectID   types.String `tfsdk:"project_id"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
	Token       types.String `tfsdk:"token"`
	TokenPrefix types.String `tfsdk:"token_prefix"`
}

var apiTokenSpec = spec[apiTokenModel]{
	name: "api_token",
	schema: schema.Schema{
		Description: "An API token of the user the provider runs as. The full token is shown by the API only once, so it is kept in the state (sensitive). " +
			"Every change replaces the token. Destroy revokes it.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttr("The token id."),
			"name": schema.StringAttribute{Required: true, Description: "Token name.", PlanModifiers: replaceMods},
			"scopes": schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Scopes: `read`, `deploy`, `write` or `admin`.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}},
			"project_id":   schema.StringAttribute{Optional: true, Description: "Limit the token to one project.", PlanModifiers: replaceMods},
			"expires_at":   schema.StringAttribute{Optional: true, Description: "Expiry time (RFC 3339). It must be in the future.", PlanModifiers: replaceMods},
			"token":        schema.StringAttribute{Computed: true, Sensitive: true, Description: "The full token.", PlanModifiers: computedKeep},
			"token_prefix": schema.StringAttribute{Computed: true, Description: "The first characters of the token.", PlanModifiers: computedKeep},
		},
	},
	create: func(ctx context.Context, c *client.Client, m *apiTokenModel) error {
		body := client.Obj{}
		put(body, "name", m.Name)
		put(body, "scopes", m.Scopes)
		put(body, "project_id", m.ProjectID)
		put(body, "expires_at", m.ExpiresAt)
		o, err := c.Post(ctx, "/tokens", body)
		if err != nil {
			return err
		}
		setStr(&m.ID, o, "id")
		setStr(&m.Token, o, "token")
		setStr(&m.TokenPrefix, o, "token_prefix")
		return nil
	},
	read: func(ctx context.Context, c *client.Client, m *apiTokenModel) (bool, error) {
		o, err := c.Find(ctx, "/tokens", "id", m.ID.ValueString())
		if client.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if client.Str(o, "revoked_at") != "" {
			return false, nil
		}
		setStr(&m.Name, o, "name")
		setSet(&m.Scopes, o, "scopes")
		optStr(&m.ProjectID, o, "project_id")
		setStr(&m.TokenPrefix, o, "token_prefix")
		return true, nil
	},
	update: func(context.Context, *client.Client, *apiTokenModel, *apiTokenModel) error {
		return errors.New("an API token cannot be changed, it must be replaced")
	},
	del: func(ctx context.Context, c *client.Client, m *apiTokenModel) error {
		return c.Delete(ctx, "/tokens/"+m.ID.ValueString())
	},
}
