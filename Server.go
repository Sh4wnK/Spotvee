package Spotvee

import (
	"github.com/Sh4wnK/Govee"
	"net/http"
)

type Server struct {
	Sessions []Govee.Govee
}

func (s *Server) newSession(w http.ResponseWriter, r *http.Request) {

}

func New() *Server {
	return &Server{}
}
