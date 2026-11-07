package dmrs

import (
	"net/http"
)

const baseLayout = "base"

func (c *Client) initRoutes() {
	r := c.router
	r.Use(c.userLoaderMiddleware)

	r.Get("/", c.h(c.indexGet))
	r.Get("/titles", c.h(c.titlesGet))
	r.Get("/titles/{id}", c.h(c.titleDetailGet))
	r.Post("/titles/{id}", c.h(c.titleDetailPost))
	r.Get(LoginPath, c.h(c.loginGetPost))
	r.Post(LoginPath, c.h(c.loginGetPost))
	r.Get("/logout", c.h(c.logoutGet))

	r.Get("/add", c.h(c.addGet))
	r.Post("/rating", c.h(c.addPost))
	r.Get("/rating/{id}", c.h(c.updateGet))
	r.Post("/rating/{id}", c.h(c.addPost))

	r.Get(RegisterPath, c.h(c.registerGetPost))
	r.Post(RegisterPath, c.h(c.registerGetPost))
	r.Get("/profile", c.h(c.profileGet))
	r.Post("/profile", c.h(c.profilePost))
	r.Get("/settings", c.h(c.settingsGet))
	r.Post("/settings", c.h(c.settingsPost))
	r.Get("/ratingset", c.h(c.ratingsetGet))
	r.Get("/upload", c.h(c.uploadGet))

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
}
