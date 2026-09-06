/*
====================================================
=====  ATS-HARDENED RESUME TEMPLATE  ===============
====================================================

A drop-in replacement for guided-resume-starter-cgc that renders text
Applicant Tracking Systems (Workday, Greenhouse, Lever, Taleo, iCIMS...)
can reliably extract.

ATS-hardening choices vs. the original template:
  - Font is Arial (a standard, universally-embedded font with clean
    ToUnicode) instead of the LaTeX font "New Computer Modern".
  - NO smallcaps anywhere. Small-caps OpenType glyphs are a leading cause
    of names/URLs being dropped or garbled by PDFBox-based parsers. The
    name and contact line are now plain, real Unicode characters.
  - Contacts render as a single, unbroken plain-text line so the parser
    keeps the email, GitHub, and LinkedIn URLs together on one line.
  - Body text is left-aligned (not justified), avoiding the irregular
    word spacing that some extractors turn into merged/split words.
  - Each experience entry keeps role, employer, and dates in a natural
    reading order.
*/

#let resume(
  author: "",
  location: "",
  contacts: (),
  body,
) = {
  set document(author: author, title: author)

  // Standard, ATS-safe font with sane fallbacks.
  set text(
    font: ("Arial", "Helvetica", "Helvetica Neue"),
    size: 10.5pt,
    lang: "en",
  )

  set page(
    margin: (top: 1.4cm, bottom: 1.1cm, left: 1.6cm, right: 1.6cm),
  )

  // Links stay dark and readable; visible text is the real URL.
  show link: set text(fill: rgb("#1a1a1a"))

  // Section headings: plain bold text (no smallcaps) + a rule. 14pt is the
  // bottom of the 14-18pt band ATS guidance asks for; at 1.05em they rendered
  // at ~11pt, barely above body text, which some parsers miss as a heading.
  show heading: it => block(above: 1.1em, below: 0.65em)[
    #set text(size: 14pt, weight: 700)
    #upper(it.body)
    #v(-0.55em)
    #line(length: 100%, stroke: 0.6pt)
  ]

  // Name — plain, bold, Title Case, real characters.
  align(center)[
    #block(text(weight: 700, size: 21pt, author))
  ]

  // Contact line — one unbroken plain-text line.
  if contacts.len() > 0 {
    align(center)[
      #block(above: 0.5em, below: 0.2em)[
        #set text(size: 9.5pt)
        #contacts.join([  |  ])
      ]
    ]
  }

  // Location on its own plain line.
  if location != "" {
    align(center)[
      #block(below: 0.2em)[#text(size: 9.5pt, location)]
    ]
  }

  set par(justify: false, leading: 0.6em)
  set list(indent: 0.6em, spacing: 0.55em)

  body
}

#let hide(should-hide, content) = {
  if not should-hide { content }
}

#let edu(
  institution: "",
  date: "",
  degrees: (),
  gpa: "",
  location: "",
) = {
  block(below: 0.9em)[
    #grid(
      columns: (1fr, auto),
      align(left)[
        #strong[#institution]#{ if gpa != "" [ #h(0.3em)|#h(0.3em) #emph[GPA: #gpa]] }
        #{
          for degree in degrees [
            \ #strong[#degree.at(0)] #h(0.3em)|#h(0.3em) #emph[#degree.at(1)]
          ]
        }
      ],
      align(right)[
        #emph[#date]#{ if location != "" [ \ #emph[#location]] }
      ],
    )
  ]
}

#let skills(areas) = {
  for area in areas {
    block(below: 0.35em)[#strong[#area.at(0): ]#area.at(1).join(", ")]
  }
}

// ATS entry — COMPANY FIRST.
//   Line 1: Employer — Location   ...   Dates   -> parsed as the COMPANY
//   Line 2: Job Title                            -> parsed as the TITLE
//   Line 3: optional one-line summary
// Two hard-won rules baked in here:
//   1. Parsers treat the FIRST line of a job block as the employer, so the
//      company must lead — title-first makes the title land in the company box.
//   2. Do NOT group multiple roles under one shared company header: Workday /
//      Textkernel then leave the company blank on every role but the first.
//      Instead, repeat the company on each role (call exp() once per role).
#let exp(
  company: "",
  location: "",
  role: "",
  date: "",
  summary: "",
  details: [],
) = {
  block(below: 0.5em, breakable: false, {
    set block(spacing: 0.3em)
    // Line 1: employer (left) + location · dates (right). This is the layout
    // that parses correctly in the ATS parsers tested. Keeping location
    // on the company line is what signals this is the EMPLOYER line — moving it
    // to its own line makes the parser read the company as the title.
    // Caveat: a bare word like "Remote" (no City, State) can still get merged
    // into the company; use a "City, State" location to avoid that.
    grid(
      columns: (1fr, auto),
      column-gutter: 1em,
      align(left, strong(company)),
      align(right, emph(
        if location != "" and date != "" [#location  ·  #date]
        else if location != "" { location }
        else { date }
      )),
    )
    // Line 2: job title.
    block(strong(role))
    if summary != "" { block(emph(summary)) }
  })
  details
}

// Projects as a CLEAN LIST — deliberately NOT job-shaped. A single intro line
// (bold name, summary, link) then bullets. The date parameter is kept for
// callers that have month-precision dates; the resume renderer does not pass
// one, because project dates are only known to the year. Because there is no separate
// employer/title/location line, parsers don't manufacture bogus job entries or
// dump the section into another role's description.
#let proj(
  name: "",
  url: "",
  date: "",
  summary: "",
  details: [],
) = {
  block(below: 0.6em, breakable: false, {
    set block(spacing: 0.28em)
    // Line 1: name + summary.
    block({
      strong(name)
      if summary != "" { [: #summary] }
    })
    // Line 2: link and year on their own line (kept off the summary).
    if url != "" or date != "" {
      block(text(size: 0.92em, {
        if url != "" { link("https://" + url)[#url] }
        if url != "" and date != "" { [ #sym.dot.c ] }
        if date != "" { emph(date) }
      }))
    }
    details
  })
}

// Patents as a single descriptive line each — NOT a job block, so parsers
// stop reading "Inventor" as a title and the patent number as an employer.
#let patent(
  id: "",
  url: "",
  date: "",
  summary: "",
) = {
  block(below: 0.4em, {
    set block(spacing: 0.28em)
    // The patent number is plain bold text, not a link. Hiding a URL behind
    // display text is the pattern ATS guidance warns about, so the address goes
    // on its own visible line below — the same shape proj() uses.
    block({
      strong(id)
      if date != "" { [ #emph[(#date)]] }
      if summary != "" { [ — #summary] }
    })
    if url != "" {
      block(text(size: 0.92em, link(url)[#url.replace("https://", "")]))
    }
  })
}
