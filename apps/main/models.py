from mongoengine.document import (
    Document, 
    EmbeddedDocument
    )
from mongoengine.fields import (
    EmbeddedDocumentField,
    StringField,
    ReferenceField,
    DecimalField,
    DateTimeField,
    )
from mongoengine.queryset.base import CASCADE
import datetime


class Account(Document):
    iban = StringField(max_length=34, primary_key=True, default="DE00000000000000000000")

    def __str__(self):
        return self.iban


class Optionee(Document):
    iban = StringField(max_length=34, primary_key=True, default="DE00000000000000000000")
    name = StringField(max_length=200, default="Max Mustermann")
    bic = StringField(max_length=11, default="BICXXXXXXXX")

    def __str__(self):
        return self.name


class Transaction(Document):
    account = ReferenceField('Account', reverse_delete_rule=CASCADE)
    date_booking = DateTimeField(default=datetime.datetime.utcnow)
    date_imported = DateTimeField(default=datetime.datetime.utcnow)
    booking_type = StringField(default="Transaction")
    optionee = ReferenceField('Optionee', reverse_delete_rule=CASCADE)
    figure = DecimalField(precision=2, default=0)
    currency = StringField(max_length=4, default="EUR")
    reference = StringField(max_length=500, default="-")

    meta = {
        "indexes": [
            {
                "fields": ["date_booking", "figure", "reference"],
                "unique": True
            }
        ]
    }

    def __str__(self):
        return str(self.figure)
