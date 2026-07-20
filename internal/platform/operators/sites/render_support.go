package sites

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

func renderNginx(site Site) (string, error) {
	names := []string{site.PrimaryDomain}
	redirects := make([]Route, 0)
	for _, route := range site.Routes {
		if route.Kind == "redirect" {
			redirects = append(redirects, route)
		} else {
			names = append(names, route.Hostname)
		}
	}
	tlsNames := []string{site.PrimaryDomain}
	if len(site.TLSDomains) > 0 {
		tlsNames = append([]string(nil), site.TLSDomains...)
	}
	data := struct {
		Site      Site
		Names     string
		TLSNames  string
		Redirects []Route
	}{Site: site, Names: strings.Join(names, " "), TLSNames: strings.Join(tlsNames, " "), Redirects: redirects}
	return executeData(nginxTemplate, data)
}

func executeData(source string, data any) (string, error) {
	tmpl, err := template.New("artifact").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse managed site template: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return "", fmt.Errorf("render managed site template: %w", err)
	}
	return output.String(), nil
}

func execute(source string, site Site) (string, error) {
	tmpl, err := template.New("artifact").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse managed site template: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, site); err != nil {
		return "", fmt.Errorf("render managed site template: %w", err)
	}
	return output.String(), nil
}
