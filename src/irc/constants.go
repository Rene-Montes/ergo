// http://tools.ietf.org/html/rfc2812
package irc

const (
	VERSION = "goircd-1"
)

const (
	RPL_WELCOME  = "001"
	RPL_YOURHOST = "002"
	RPL_CREATED  = "003"
	RPL_MYINFO   = "004"
	RPL_UMODEIS  = "221"
	RPL_NONE     = "300"
)

const (
	ERR_NOSUCHNICK       = "401"
	ERR_NOSUCHSERVER     = "402"
	ERR_NOSUCHCHANNEL    = "403"
	ERR_UNKNOWNCOMMAND   = "421"
	ERR_NICKNAMEINUSE    = "433"
	ERR_NEEDMOREPARAMS   = "461"
	ERR_ALREADYREGISTRED = "462"
	ERR_USERSDONTMATCH   = "502"
)
