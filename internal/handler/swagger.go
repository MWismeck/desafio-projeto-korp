package handler

import (
	"net/http"

	httpSwagger "github.com/swaggo/http-swagger/v2"

	// Registers the generated OpenAPI document that the UI below serves as doc.json.
	_ "github.com/MWismeck/desafio-projeto-korp/api"
)

// SwaggerPath is the prefix under which the Swagger UI and its document are served.
const SwaggerPath = "/swagger/"

// Swagger serves the Swagger UI and the generated document under SwaggerPath.
func Swagger() http.Handler {
	return httpSwagger.Handler(httpSwagger.URL(SwaggerPath + "doc.json"))
}
