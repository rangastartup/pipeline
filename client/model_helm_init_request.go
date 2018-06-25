/*
 * Pipeline API
 *
 * Pipeline v0.3.0 swagger
 *
 * API version: 0.3.0
 * Contact: info@banzaicloud.com
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package client

type HelmInitRequest struct {
	KubeContext string `json:"kube_context,omitempty"`
	Namespace string `json:"namespace"`
	Upgrade bool `json:"upgrade,omitempty"`
	ServiceAccount string `json:"service_account"`
	CanaryImage bool `json:"canary_image,omitempty"`
	TillerImage string `json:"tiller_image,omitempty"`
	HistoryMax int32 `json:"history_max,omitempty"`
}