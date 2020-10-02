from mongoengine.document import Document
from mongoengine.fields import (
    StringField,
    ReferenceField,
    DecimalField,
    DateTimeField,
    )
from mongoengine.queryset.base import CASCADE


class Account(Document):
    iban = StringField(max_length=22, unique=True)

    def __str__(self):
        return self.iban


class Optionee(Document):
    iban = StringField(max_length=22, unique=True)
    name = StringField(max_length=200)
    bic = StringField(max_length=11)

    def __str__(self):
        return self.name


class Transaction(Document):
    account = ReferenceField('Account', reverse_delete_rule=CASCADE)
    date_booking = DateTimeField()
    date_imported = DateTimeField(auto_now_add=True)
    booking_type = StringField()
    optionee = ReferenceField('Optionee', reverse_delete_rule=CASCADE)
    figure = DecimalField(precision=2)
    currency = StringField(max_length=4)
    reference = StringField(max_length=500)

    def __str__(self):
        return str(self.figure)
