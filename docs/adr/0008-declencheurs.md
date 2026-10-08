# Un déclencheur demande une passe, jamais le traitement d'un ticket

Remplace l'ADR 0001 sur un point : night-shift ne réagit plus seulement à une échéance. Un poste déclare un **déclencheur**, union d'un seul membre : une échéance cron, une demande manuelle, plus tard un événement de la forge ou du tracker (pose de `ready-for-agent`, commentaire de revue). Quel que soit le membre, le déclencheur **demande une passe** sur la source de tickets du poste. Il ne lance jamais un ticket isolé. Si une passe est déjà active sur cette source, la demande se fond dans une seule passe en attente, lancée à la fin de la passe active ; d'autres demandes reçues entre-temps n'en ajoutent pas.

L'ADR 0001 reste valable pour l'essentiel : une seule passe active par source, une file parcourue par priorité puis ancienneté, un plafond et un digest par passe.

## Considered Options

- **Cron seul** (ADR 0001). Écarté : le travail délégué le jour attendrait la nuit, alors que rien dans le modèle ne l'impose.
- **Un événement lance le ticket qui l'a porté**, comme une usine interactive. Écarté : cela réintroduit la concurrence entre agents que la sérialisation supprimait, et dissout la passe, donc son plafond et son digest.

## Consequences

- Ordre de livraison : cron, puis demande manuelle (la création manuelle d'une Passe prévue par l'ADR 0004), puis événement.
- Le déclencheur par événement exige que la forge ou le tracker joigne le cluster par webhook, donc une entrée exposée ; il viendra avec son propre examen de cette exposition.
