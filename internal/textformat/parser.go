package textformat

// TODO: these should be methods, so we can collect errors somewhere
func parseModule(sx *sexpr) *Module {
	if sx.HeadKeyword() != "module" {
		// TODO proper error
		return nil
	}

	m := &Module{}
	cursor := 1
	if len(sx.list) > 1 && sx.list[1].tok.name == ID {
		m.Name = sx.list[1].tok.value
		m.loc = sx.list[1].tok.loc
		cursor++
	}

	for i := cursor; i < len(sx.list); i++ {
		sub := sx.list[i]
		if sub.HeadKeyword() == "func" {
			m.Funcs = append(m.Funcs, parseFunction(sub))
		}
		// TODO: check all other types too
	}

	return m
}

func parseFunction(sx *sexpr) *Function {
	return nil
}
