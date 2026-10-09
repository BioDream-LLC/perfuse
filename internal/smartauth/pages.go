package smartauth

import "html/template"

// The sign-in pages: plain forms, labelled for screen readers, readable at any width, no scripts.
var pages = template.Must(template.New("").Parse(`
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.}}</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:2rem auto;padding:0 1rem;color:#1a1a1a}
label{display:block;margin-top:1rem}input[type=text],input[type=password],input[type=search]{width:100%;padding:.5rem;font:inherit}
button{margin-top:1.25rem;padding:.6rem 1.2rem;font:inherit}.error{color:#a00000;font-weight:600}
fieldset{border:1px solid #767676;padding:.5rem 1rem}li{margin:.25rem 0}</style></head><body><main>{{end}}
{{define "foot"}}</main></body></html>{{end}}

{{define "problem"}}{{template "head" "Sign-in problem"}}<h1>Sign-in problem</h1><p role="alert">{{.}}</p>{{template "foot"}}{{end}}

{{define "signin"}}{{template "head" "Sign in"}}<h1>Sign in</h1>
<p><strong>{{.App}}</strong> is asking to use your health records.</p>
{{with .Error}}<p class="error" role="alert">{{.}}</p>{{end}}
<form method="post" action="signin"><input type="hidden" name="req" value="{{.Req}}">
<label for="username">Username</label><input type="text" id="username" name="username" autocomplete="username" required autofocus>
<label for="password">Password</label><input type="password" id="password" name="password" autocomplete="current-password" required>
<button type="submit">Sign in</button></form>
{{with .Upstream}}<form method="post" action="oidc/start"><input type="hidden" name="req" value="{{$.Req}}">
<button type="submit">Sign in with {{.}}</button></form>{{end}}{{template "foot"}}{{end}}

{{define "patient"}}{{template "head" "Choose a patient"}}<h1>Choose a patient</h1>
<p><strong>{{.App}}</strong> works with one patient's records. Choose whose.</p>
<form method="post" action="patient"><input type="hidden" name="req" value="{{.Req}}">
<label for="search">Search by name</label><input type="search" id="search" name="search" value="{{.Search}}">
<button type="submit">Search</button></form>
<form method="post" action="patient"><input type="hidden" name="req" value="{{.Req}}">
<fieldset><legend>Patients</legend>{{range .Patients}}
<label><input type="radio" name="patient" value="{{.ID}}" required> {{.Name}}{{with .BirthDate}}, born {{.}}{{end}} ({{.ID}})</label>
{{else}}<p>No patients found.</p>{{end}}</fieldset>
<button type="submit">Continue</button></form>{{template "foot"}}{{end}}

{{define "consent"}}{{template "head" "Allow access"}}<h1>Allow access</h1>
<p><strong>{{.App}}</strong> is asking for access{{with .Patient}} to the records of patient {{.}}{{end}}. Untick anything you do not want to share.</p>
<form method="post" action="consent"><input type="hidden" name="req" value="{{.Req}}">
<fieldset><legend>The app may</legend><ul>{{range .Scopes}}
<li><label><input type="checkbox" name="scope" value="{{.Scope}}" checked> {{.Text}}</label></li>{{end}}</ul></fieldset>
<button type="submit" name="action" value="allow">Allow</button>
<button type="submit" name="action" value="deny">Deny</button></form>{{template "foot"}}{{end}}

{{define "launch-signin"}}{{template "head" "Launch an app"}}<h1>Launch an app</h1>
<p>Sign in to open an app with a patient's record, as an EHR would from the chart.</p>
{{with .Error}}<p class="error" role="alert">{{.}}</p>{{end}}
<form method="post" action="launch/signin">
<label for="username">Username</label><input type="text" id="username" name="username" autocomplete="username" required autofocus>
<label for="password">Password</label><input type="password" id="password" name="password" autocomplete="current-password" required>
<button type="submit">Sign in</button></form>{{template "foot"}}{{end}}

{{define "launch"}}{{template "head" "Launch an app"}}<h1>Launch an app</h1>
<form method="post" action="choose"><input type="hidden" name="req" value="{{.Req}}">
<label for="search">Search patients by name</label><input type="search" id="search" name="search" value="{{.Search}}">
<button type="submit">Search</button></form>
<form method="post" action="choose"><input type="hidden" name="req" value="{{.Req}}">
<fieldset><legend>App</legend>{{range .Apps}}
<label><input type="radio" name="app" value="{{.ID}}" required> {{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</label>
{{else}}<p>No app has a launch_url registered.</p>{{end}}</fieldset>
<fieldset><legend>Patient</legend>{{range .Patients}}
<label><input type="radio" name="patient" value="{{.ID}}" required> {{.Name}}{{with .BirthDate}}, born {{.}}{{end}} ({{.ID}})</label>
{{else}}<p>No patients found.</p>{{end}}</fieldset>
<label for="encounter">Encounter id (optional)</label><input type="text" id="encounter" name="encounter">
<button type="submit">Launch</button></form>{{template "foot"}}{{end}}
`))
