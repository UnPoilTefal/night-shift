# Passes planifiées et sérialisées plutôt que déclenchement par événement

Les tickets prêts sont traités par une passe planifiée qui parcourt la file (priorité, puis ancienneté, sans les tickets bloqués) et en traite au plus un nombre plafonné, avec une seule passe active à la fois par périmètre. On ne réagit pas à la pose du label `ready-for-agent`. La sérialisation supprime par construction la concurrence entre agents sur un même ticket, et la file permet priorités, plafonds et digest. Le déclenchement par événement reste une évolution envisagée au palier équipe, une fois la réservation fiabilisée.

Origine : entretien de cadrage du 2026-10-03, à partir de l'état de l'art `UnPoilTefal/homelab` `docs/research/2026-10-03-agents-autonomes-tickets.md`.
