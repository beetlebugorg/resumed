/*
====================================================
=====  COVER LETTER TEMPLATE  ======================
====================================================

This deliberately does NOT reuse the resume's header. A resume opens with
a large centred name because the reader is scanning for identity; a letter
opens quietly because the reader is about to read prose. Borrowing the CV
header made the letter look like a resume with paragraphs pasted into it.

Block-format business letter:
  - Modest left-aligned letterhead. Name at reading size, contacts on one
    small line beneath. No centring, no rule, no 21pt type.
  - Date, recipient, and salutation each on their own line.
  - Paragraphs separated by space rather than indented, which is the
    convention for block format and easier to skim.
  - Signature block at the end, with room to sign.

Shares the resume's font and link colour so the two still read as one set
when they arrive together, but nothing else.
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
  set document(author: author, title: author + " Cover Letter")

  set text(
    font: ("Arial", "Helvetica", "Helvetica Neue"),
    size: 10.5pt,
    lang: "en",
  )

  // Roomier than the resume. A resume fights for space; a letter should
  // look unhurried.
  set page(
    margin: (top: 1.9cm, bottom: 1.5cm, left: 1.9cm, right: 1.9cm),
  )

  show link: set text(fill: rgb("#1a1a1a"))

  set par(justify: false, leading: 0.62em, first-line-indent: 0pt)
  set block(spacing: 0.95em)

  // Letterhead: reading size, not display size.
  block(below: 0.35em)[#text(size: 13pt, weight: 700, author)]

  {
    let items = contacts
    if location != "" { items = (..contacts, location) }
    if items.len() > 0 {
      block(below: 1.9em)[
        #set text(size: 8.8pt, fill: rgb("#444444"))
        #items.join([  ·  ])
      ]
    }
  }

  if date != "" {
    block(below: 1.4em)[#date]
  }

  // Only what the posting actually told us. Inventing a hiring manager's
  // name is the kind of fabrication this tool exists to prevent.
  if recipient.len() > 0 {
    block(below: 1.4em)[
      #for line in recipient [#line \ ]
    ]
  }

  if greeting != "" {
    block(below: 1.0em)[#greeting]
  }

  body

  if closing != "" {
    block(above: 1.5em, below: 2.6em)[#closing]
    block[#author]
  }
}
