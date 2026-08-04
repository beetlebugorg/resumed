/*
====================================================
=====  COVER LETTER TEMPLATE  ======================
====================================================

Deliberately matched to ats-resume.typ: same font, same margins, same
centred name and contact line. A letter and a resume arriving together
should look like one document set, not two.

Kept plain for the same reasons the resume is: real Unicode characters,
no smallcaps, left-aligned body, links rendered as their visible text.
Some employers run cover letters through the same parsers as resumes.
*/

#let cover-letter(
  author: "",
  location: "",
  contacts: (),
  date: "",
  recipient: (),
  greeting: "",
  closing: "",
  body,
) = {
  set document(author: author, title: author + " — Cover Letter")

  set text(
    font: ("Arial", "Helvetica", "Helvetica Neue"),
    size: 10.5pt,
    lang: "en",
  )

  set page(
    margin: (top: 1.4cm, bottom: 1.1cm, left: 1.6cm, right: 1.6cm),
  )

  show link: set text(fill: rgb("#1a1a1a"))

  // Header block, identical in feel to the resume.
  align(center)[
    #block(text(weight: 700, size: 21pt, author))
  ]

  if contacts.len() > 0 {
    align(center)[
      #block(above: 0.5em, below: 0.2em)[
        #set text(size: 9.5pt)
        #contacts.join([  |  ])
      ]
    ]
  }

  if location != "" {
    align(center)[
      #block(below: 0.2em)[#text(size: 9.5pt, location)]
    ]
  }

  // Rule under the header separates letterhead from letter.
  block(above: 0.8em, below: 1.2em)[#line(length: 100%, stroke: 0.6pt)]

  set par(justify: false, leading: 0.65em, first-line-indent: 0pt)

  if date != "" {
    block(below: 1.0em)[#date]
  }

  // Recipient lines, one per entry. Absent for most online applications,
  // which is why it is optional rather than a required address block.
  if recipient.len() > 0 {
    block(below: 1.0em)[
      #for line in recipient [#line \ ]
    ]
  }

  if greeting != "" {
    block(below: 0.9em)[#greeting]
  }

  // Paragraphs are spaced rather than indented — the convention for a
  // block-format business letter, and easier to skim.
  set block(spacing: 0.9em)
  body

  if closing != "" {
    block(above: 1.2em, below: 0.2em)[#closing]
    block[#author]
  }
}
