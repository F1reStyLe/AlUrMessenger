/* Swagger settings are fixed by the application, never by query-string configuration.
 * Tokens live only in this tab's memory; no cookies/local storage or external validator.
 * The CSP also blocks network destinations outside the current API origin.
 */
window.addEventListener("DOMContentLoaded", () => {
  SwaggerUIBundle({
    url: "/docs/api/openapi.json",
    dom_id: "#swagger-ui",
    presets: [SwaggerUIBundle.presets.apis],
    layout: "BaseLayout",
    deepLinking: true,
    docExpansion: "list",
    defaultModelsExpandDepth: -1,
    persistAuthorization: false,
    withCredentials: false,
    queryConfigEnabled: false,
    validatorUrl: null,
    supportedSubmitMethods: ["get", "head", "patch"],
    // Browser-owned Origin preflight cannot be meaningfully invoked by Try it out.
    displayRequestDuration: true
  });
});
