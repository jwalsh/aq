;;; aq-tramp.el --- TRAMP-aware aq presence layer for Emacs -*- lexical-binding: t; -*-

;; Author: Jason Walsh <j@wal.sh>
;; URL: https://github.com/jwalsh/aq
;; Version: 0.1.0
;; Package-Requires: ((emacs "28.1") (json "1.5"))
;; Keywords: tools, processes, convenience

;;; Commentary:

;; aq-tramp.el makes aq's gossip layer transparent to Emacs via TRAMP.
;;
;; The core insight: TRAMP is Transparent Remote Access, Multiple Protocols.
;; aq is Transparent Remote Presence, Multiple Transports.  The design
;; philosophies are identical — the consumer should never change behavior
;; based on where data lives or how it propagated.
;;
;; This package provides:
;;
;; - `aq-announce' — broadcast presence from Emacs, writing to AQ_HOME
;;   via TRAMP (local, SSH, Docker, Bastille, Kubernetes — all transparent)
;;
;; - `aq-status' — read active broadcasts from any TRAMP-accessible
;;   AQ_HOME and display them in an Emacs buffer
;;
;; - `aq-check' — detect conflicts against active broadcasts
;;
;; - `aq-watch' — use `file-notify' (which delegates to TRAMP's
;;   file-notify backend) to watch for new broadcasts
;;
;; - `aq-multi-home' — fan out presence to multiple AQ_HOME directories,
;;   like TRAMP's multi-hop but in parallel
;;
;; Usage:
;;
;;   ;; Local (Tier 0)
;;   (setq aq-home "~/.aq")
;;
;;   ;; Remote via SSH (Tier 0.5)
;;   (setq aq-home "/ssh:nexus:/home/jasonwalsh/.aq")
;;
;;   ;; FreeBSD jail via TRAMP bastille method (Tier 0.5)
;;   (setq aq-home "/bastille:agent-alpha:/root/.aq")
;;
;;   ;; Kubernetes pod (Tier 0.5)
;;   (setq aq-home "/kubernetes:aq-worker%aq-system:/tmp/.aq")
;;
;;   ;; Multi-home fan-out (parallel broadcast)
;;   (setq aq-homes '("~/.aq"
;;                     "/ssh:nexus:/home/jasonwalsh/.aq"
;;                     "/bastille:agent-alpha:/root/.aq"))
;;
;; Then:
;;   M-x aq-announce   — broadcast what you're working on
;;   M-x aq-status     — see who else is broadcasting
;;   M-x aq-check      — check for conflicts
;;   M-x aq-watch      — start watching for broadcasts (file-notify)
;;
;; The TRAMP Principle:
;;
;;   Every function in this package calls `find-file-noselect',
;;   `write-region', `directory-files', or `file-notify-add-watch'.
;;   These are Emacs file primitives that TRAMP makes transparent.
;;   There is zero network code here.  The transport is invisible.

;;; Code:

(require 'json)
(require 'filenotify)

;;;; Custom variables

(defgroup aq nil
  "Ambient agent queue — gossip layer for multi-agent development."
  :group 'tools
  :prefix "aq-")

(defcustom aq-home (or (getenv "AQ_HOME")
                       (expand-file-name ".aq" (getenv "HOME")))
  "Primary AQ_HOME directory.  Any TRAMP path works transparently.
Examples:
  \"~/.aq\"                                    — local filesystem
  \"/ssh:nexus:/home/jasonwalsh/.aq\"          — remote via SSH
  \"/bastille:agent-alpha:/root/.aq\"          — FreeBSD jail
  \"/kubernetes:aq-worker%aq-system:/tmp/.aq\" — k8s pod"
  :type 'directory
  :group 'aq)

(defcustom aq-homes nil
  "List of AQ_HOME directories for multi-transport fan-out.
When non-nil, `aq-announce' writes to ALL of these.
When nil, only `aq-home' is used.

This is the TRAMP multi-hop principle rotated 90°: instead of
chaining protocols in series, we fan them out in parallel."
  :type '(repeat directory)
  :group 'aq)

(defcustom aq-default-ttl 3600
  "Default TTL in seconds.  3600s = 1 hour, matching real session length.
The original 300s caused \"gossip with amnesia\" — all broadcasts
expired while agents were still working."
  :type 'integer
  :group 'aq)

(defcustom aq-default-channel "broadcast"
  "Default channel name."
  :type 'string
  :group 'aq)

(defcustom aq-agent-name nil
  "Agent name for broadcasts.  Auto-detected from git if nil."
  :type '(choice (const nil) string)
  :group 'aq)

;;;; Internal helpers

(defun aq--channel-dir (home &optional channel)
  "Return the requests directory for CHANNEL in HOME."
  (let ((ch (or channel aq-default-channel)))
    (expand-file-name (format "channels/%s/requests" ch) home)))

(defun aq--archive-dir (home &optional channel)
  "Return the archive directory for CHANNEL in HOME."
  (let ((ch (or channel aq-default-channel)))
    (expand-file-name (format "channels/%s/archive" ch) home)))

(defun aq--ulid ()
  "Generate a ULID-like identifier (timestamp + random).
Not a true ULID but lexicographically sortable and unique enough."
  (let* ((now (float-time))
         (ts (format "%014d" (truncate (* now 1000))))
         (rand (format "%010x" (random (expt 16 10)))))
    (concat ts rand)))

(defun aq--agent-name ()
  "Return the agent name, auto-detecting from git if not set."
  (or aq-agent-name
      (let ((branch (string-trim
                     (shell-command-to-string "git rev-parse --abbrev-ref HEAD 2>/dev/null")))
            (remote (string-trim
                     (shell-command-to-string "git remote get-url origin 2>/dev/null"))))
        (if (and (not (string-empty-p remote))
                 (not (string-empty-p branch)))
            (format "%s/%s" remote branch)
          (format "%s/%s" (system-name) (user-login-name))))))

(defun aq--homes ()
  "Return the list of AQ_HOME directories to write to."
  (or aq-homes (list aq-home)))

(defun aq--broadcast-to-json (broadcast)
  "Encode BROADCAST alist to JSON string."
  (json-encode broadcast))

(defun aq--json-to-broadcast (json-str)
  "Decode JSON-STR to broadcast alist."
  (condition-case nil
      (json-read-from-string json-str)
    (error nil)))

(defun aq--broadcast-expired-p (broadcast)
  "Return non-nil if BROADCAST has expired."
  (let ((ts (alist-get 'ts broadcast))
        (ttl (alist-get 'ttl broadcast)))
    (when (and ts ttl)
      (> (float-time) (+ ts ttl)))))

(defun aq--ensure-dirs (home)
  "Ensure channel directories exist in HOME.
This works transparently over TRAMP — `make-directory' on a TRAMP
path creates the remote directory."
  (let ((req-dir (aq--channel-dir home))
        (arch-dir (aq--archive-dir home)))
    (unless (file-directory-p req-dir)
      (make-directory req-dir t))
    (unless (file-directory-p arch-dir)
      (make-directory arch-dir t))))

;;;; Core operations — all use Emacs file primitives (TRAMP-transparent)

(defun aq--write-broadcast (home broadcast)
  "Write BROADCAST to HOME's channel directory.
Uses `write-region' which TRAMP makes transparent — this function
works identically for local paths, SSH, Docker, Bastille, k8s."
  (aq--ensure-dirs home)
  (let* ((id (alist-get 'id broadcast))
         (ts (format "%014d" (truncate (* (alist-get 'ts broadcast) 1000))))
         (filename (format "aq-%s-%s.json" ts id))
         (filepath (expand-file-name filename (aq--channel-dir home)))
         (json (aq--broadcast-to-json broadcast)))
    (with-temp-buffer
      (insert json "\n")
      (write-region (point-min) (point-max) filepath nil 'quiet))
    filepath))

(defun aq--read-active (home &optional channel)
  "Read active (non-expired) broadcasts from HOME.
Uses `directory-files' and `insert-file-contents' — both TRAMP-transparent.
Expired broadcasts are archived via `rename-file' (also TRAMP-transparent)."
  (let* ((req-dir (aq--channel-dir home channel))
         (arch-dir (aq--archive-dir home channel))
         (active '()))
    (when (file-directory-p req-dir)
      (dolist (file (directory-files req-dir t "\\.json$"))
        (condition-case nil
            (let* ((content (with-temp-buffer
                              (insert-file-contents file)
                              (buffer-string)))
                   (broadcast (aq--json-to-broadcast content)))
              (if (aq--broadcast-expired-p broadcast)
                  ;; Archive expired — rename-file is TRAMP-transparent
                  (condition-case nil
                      (rename-file file
                                   (expand-file-name (file-name-nondirectory file)
                                                     arch-dir))
                    (error nil))  ; Already archived by another reader
                (push broadcast active)))
          (error nil))))  ; Skip malformed files
    (nreverse active)))

;;;; Interactive commands

;;;###autoload
(defun aq-announce (conjecture-id claim phase files &optional ttl)
  "Announce presence: working on CONJECTURE-ID with CLAIM in PHASE touching FILES.
Writes to all AQ_HOME directories (multi-transport fan-out).
TTL defaults to `aq-default-ttl'.

This is the TRAMP principle in action: the same function writes to
a local directory, a remote SSH path, a FreeBSD jail, or a k8s pod
with zero code changes."
  (interactive
   (list (read-string "Conjecture ID (e.g. C-1): ")
         (read-string "Claim: ")
         (completing-read "Phase: " '("conjecture" "proof" "refutation" "refinement"))
         (read-string "Files (comma-separated): ")
         nil))
  (let* ((id (aq--ulid))
         (broadcast `((id . ,id)
                      (agent . ,(aq--agent-name))
                      (worktree . ,(string-trim
                                    (shell-command-to-string
                                     "git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown")))
                      (conjecture_id . ,conjecture-id)
                      (conjecture_claim . ,claim)
                      (phase . ,phase)
                      (status . "prosecuting")
                      (files . ,(vconcat (split-string files "," t " ")))
                      (ts . ,(float-time))
                      (ttl . ,(or ttl aq-default-ttl))))
         (homes (aq--homes))
         (paths '()))
    ;; Fan-out: write to every AQ_HOME
    (dolist (home homes)
      (condition-case err
          (push (aq--write-broadcast home broadcast) paths)
        (error (message "aq: failed to write to %s: %s" home err))))
    (message "aq: announced %s to %d home(s): %s"
             conjecture-id (length paths)
             (mapconcat #'identity paths ", "))
    broadcast))

;;;###autoload
(defun aq-status ()
  "Display active broadcasts from all AQ_HOME directories.
Reads via TRAMP-transparent `directory-files' + `insert-file-contents'."
  (interactive)
  (let ((all-broadcasts '()))
    (dolist (home (aq--homes))
      (condition-case nil
          (let ((active (aq--read-active home)))
            (dolist (b active)
              (push (cons (cons 'aq_home home) b) all-broadcasts)))
        (error nil)))
    (if (null all-broadcasts)
        (message "aq: no active broadcasts")
      (with-current-buffer (get-buffer-create "*aq-status*")
        (let ((inhibit-read-only t))
          (erase-buffer)
          (insert (format "aq status — %d active broadcast(s)\n"
                          (length all-broadcasts)))
          (insert (make-string 60 ?─) "\n\n")
          (dolist (b (reverse all-broadcasts))
            (let ((agent (alist-get 'agent b))
                  (conj (alist-get 'conjecture_id b))
                  (claim (alist-get 'conjecture_claim b))
                  (phase (alist-get 'phase b))
                  (status (alist-get 'status b))
                  (files (alist-get 'files b))
                  (ts (alist-get 'ts b))
                  (ttl (alist-get 'ttl b))
                  (home (alist-get 'aq_home b)))
              (let* ((expires (+ ts ttl))
                     (remaining (max 0 (truncate (- expires (float-time))))))
                (insert (format "  %s [%s] %s\n" agent conj phase))
                (insert (format "    claim: %s\n" claim))
                (insert (format "    files: %s\n" (mapconcat #'identity (append files nil) ", ")))
                (insert (format "    status: %s  TTL: %ds remaining\n" status remaining))
                (when home
                  (insert (format "    home: %s\n" home)))
                (insert "\n")))))
        (goto-char (point-min))
        (special-mode)
        (display-buffer (current-buffer))))))

;;;###autoload
(defun aq-check (conjecture-id files)
  "Check for conflicts: does anyone overlap with CONJECTURE-ID on FILES?
Reads from all AQ_HOME directories via TRAMP."
  (interactive
   (list (read-string "Conjecture ID: ")
         (read-string "Files (comma-separated): ")))
  (let ((my-files (split-string files "," t " "))
        (conflicts '()))
    (dolist (home (aq--homes))
      (condition-case nil
          (dolist (b (aq--read-active home))
            (when (and (not (string= (alist-get 'status b) "done"))
                       (not (string= (alist-get 'agent b) (aq--agent-name))))
              (let* ((their-files (append (alist-get 'files b) nil))
                     (overlap (seq-intersection my-files their-files #'string=)))
                (when overlap
                  (let* ((their-phase (alist-get 'phase b))
                         (severity (cond
                                    ((string= their-phase "proof") "HIGH")
                                    ((string= their-phase "conjecture") "MEDIUM")
                                    (t "LOW"))))
                    (push (list :agent (alist-get 'agent b)
                                :conjecture (alist-get 'conjecture_id b)
                                :severity severity
                                :overlap overlap
                                :home home)
                          conflicts))))))
        (error nil)))
    (if (null conflicts)
        (message "aq: no conflicts detected")
      (message "aq: %d conflict(s) detected" (length conflicts))
      (dolist (c conflicts)
        (message "  [%s] %s (%s) — shared: %s"
                 (plist-get c :severity)
                 (plist-get c :agent)
                 (plist-get c :conjecture)
                 (mapconcat #'identity (plist-get c :overlap) ", "))))
    conflicts))

;;;###autoload
(defun aq-watch ()
  "Watch for new broadcasts using `file-notify'.
`file-notify-add-watch' delegates to TRAMP's file-notify backend
when watching a remote directory — inotify/kqueue/FSEvents are
replaced by periodic polling over the TRAMP connection.  Same
interface, different transport.  The TRAMP principle."
  (interactive)
  (dolist (home (aq--homes))
    (let ((dir (aq--channel-dir home)))
      (when (file-directory-p dir)
        (file-notify-add-watch
         dir '(change)
         (lambda (event)
           (when (eq (nth 1 event) 'created)
             (let* ((file (nth 2 event))
                    (content (condition-case nil
                                 (with-temp-buffer
                                   (insert-file-contents file)
                                   (buffer-string))
                               (error nil)))
                    (broadcast (when content
                                 (aq--json-to-broadcast content))))
               (when broadcast
                 (message "aq: new broadcast from %s [%s] — %s"
                          (alist-get 'agent broadcast)
                          (alist-get 'conjecture_id broadcast)
                          (alist-get 'conjecture_claim broadcast)))))))
        (message "aq: watching %s" dir)))))

;;;###autoload
(defun aq-init ()
  "Initialize AQ_HOME directory structure.
Creates channels/broadcast/requests/ and channels/broadcast/archive/
at each configured AQ_HOME.  Works over TRAMP."
  (interactive)
  (dolist (home (aq--homes))
    (aq--ensure-dirs home)
    (message "aq: initialized %s" home)))

(provide 'aq-tramp)

;;; aq-tramp.el ends here
