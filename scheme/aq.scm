;;; aq.scm — Guile Scheme port of aq (ambient agent queue)
;;;
;;; Wire format compatible with the Go implementation.
;;; Reads and writes the same NDJSON files in ~/.aq/channels/broadcast/
;;;
;;; Contract: the broadcast schema is defined in CLAUDE.md.
;;; This port MUST produce identical JSON to the Go binary.
;;; If the Go side changes the wire format, this breaks silently.
;;;
;;; Bead: aq-dde
;;; Assignee: dsp-dr
;;; Conjectures: C-1 (filesystem transport), C-3 (NDJSON+TTL sufficient)

(use-modules (json)           ; guile-json
             (srfi srfi-19)   ; date/time
             (ice-9 ftw)      ; filesystem traversal
             (ice-9 match)    ; pattern matching
             (ice-9 format))  ; formatted output

;;; ---------- Configuration ----------

(define (aq-home)
  "Return AQ_HOME: env var > .aq/ in cwd > ~/.aq/"
  (or (getenv "AQ_HOME")
      (and (file-exists? ".aq")
           (stat:type (stat ".aq")) ; verify it's a directory
           (string-append (getcwd) "/.aq"))
      (string-append (getenv "HOME") "/.aq")))

(define (broadcast-dir)
  (string-append (aq-home) "/channels/broadcast/requests"))

(define (archive-dir)
  (string-append (aq-home) "/channels/broadcast/archive"))

;;; ---------- ULID (compatible with Go implementation) ----------
;;; Go uses: 12 hex chars ms timestamp + 10 random lowercase alphanumeric
;;; This is NOT a real ULID — it matches the Go port's format exactly.

(define ulid-chars "0123456789abcdefghijklmnopqrstuvwxyz")

(define (generate-ulid)
  "Generate a ULID matching the Go implementation's format."
  (let* ((ms (inexact->exact (truncate (* (current-time) 1000))))
         (ts-hex (format #f "~12,'0x" ms))
         (rand-part (list->string
                     (map (lambda (_)
                            (string-ref ulid-chars
                                        (random (string-length ulid-chars))))
                          (iota 10)))))
    (string-append ts-hex rand-part)))

;;; ---------- Broadcast ----------

(define* (make-broadcast #:key
                         agent worktree conjecture-id
                         (conjecture-claim "")
                         (phase "conjecture")
                         (status "prosecuting")
                         (files '())
                         (ttl 300))
  "Create a broadcast association list matching Go wire format."
  `((agent . ,agent)
    (worktree . ,worktree)
    (conjecture_id . ,conjecture-id)
    (conjecture_claim . ,conjecture-claim)
    (phase . ,phase)
    (status . ,status)
    (files . ,(list->vector files))
    (ts . ,(exact->inexact (current-time)))
    (ttl . ,ttl)
    (id . ,(generate-ulid))))

;;; ---------- Storage ----------

(define (announce! broadcast)
  "Write a broadcast to the filesystem. Returns the filename."
  (let* ((ts-part (format #f "~14,'0d"
                          (inexact->exact
                           (truncate (* (assoc-ref broadcast 'ts) 1000)))))
         (id (assoc-ref broadcast 'id))
         (filename (format #f "aq-~a-~a.json" ts-part id))
         (filepath (string-append (broadcast-dir) "/" filename)))
    ;; Ensure directory exists
    (system* "mkdir" "-p" (broadcast-dir))
    (call-with-output-file filepath
      (lambda (port)
        (scm->json broadcast port)
        (newline port)))
    filename))

(define (read-active)
  "Read all non-expired broadcasts. Archive expired ones."
  (let* ((dir (broadcast-dir))
         (now (current-time)))
    (if (not (file-exists? dir))
        '()
        (let ((files (scandir dir
                              (lambda (f)
                                (and (string-suffix? ".json" f)
                                     (string-prefix? "aq-" f))))))
          (filter-map
           (lambda (f)
             (let* ((filepath (string-append dir "/" f))
                    (broadcast (call-with-input-file filepath json->scm))
                    (ts (assoc-ref broadcast "ts"))
                    (ttl (assoc-ref broadcast "ttl")))
               (if (> (+ ts ttl) now)
                   broadcast  ; still active
                   (begin
                     ;; Archive expired
                     (system* "mkdir" "-p" (archive-dir))
                     (rename-file filepath
                                  (string-append (archive-dir) "/" f))
                     #f))))
           (or files '()))))))

;;; ---------- Conflict Detection ----------

(define (check-conflicts broadcast active-broadcasts)
  "Check for file-overlap conflicts modulated by CPRR phase."
  (filter-map
   (lambda (other)
     (let* ((shared (lset-intersection
                     string=?
                     (vector->list
                      (or (assoc-ref broadcast 'files) #()))
                     (vector->list
                      (or (assoc-ref other "files") #()))))
            (severity (cond
                       ((null? shared) #f)
                       ((and (string=? (or (assoc-ref broadcast 'phase) "")
                                       "proof")
                             (string=? (or (assoc-ref other "phase") "")
                                       "proof"))
                        "HIGH")
                       ((or (string=? (or (assoc-ref broadcast 'phase) "")
                                      "proof")
                            (string=? (or (assoc-ref other "phase") "")
                                      "proof"))
                        "MEDIUM")
                       (else "LOW"))))
       (and severity
            `((severity . ,severity)
              (agent . ,(assoc-ref other "agent"))
              (conjecture_id . ,(assoc-ref other "conjecture_id"))
              (shared_files . ,shared)))))
   active-broadcasts))

;;; ---------- CLI (stub — expand for full CLI) ----------

(define (main args)
  "Entry point. Dispatch on first argument."
  (match (cdr args)  ; skip program name
    (("announce" . rest)
     (format #t "TODO: parse CLI args and call announce!~%"))
    (("status" . rest)
     (let ((active (read-active)))
       (if (null? active)
           (format #t "no active broadcasts~%")
           (for-each
            (lambda (b)
              (format #t "~a ~a [~a] ~a~%"
                      (or (assoc-ref b "agent") "?")
                      (or (assoc-ref b "conjecture_id") "?")
                      (or (assoc-ref b "phase") "?")
                      (or (assoc-ref b "files") "?")))
            active))))
    (("version" . _)
     (format #t "aq-scheme 0.1.0~%"))
    (("help" . _)
     (format #t "aq-scheme — Guile Scheme port of aq~%~%")
     (format #t "Commands: announce, status, check, version, help~%"))
    (_
     (format #t "aq-scheme: unknown command~%")
     (format #t "Run 'aq-scheme help' for usage.~%"))))

;; Run if executed directly
;; (main (command-line))
